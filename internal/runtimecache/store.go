package runtimecache

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/supply"
)

// Store is an owned directory held open for the lifetime of its operations.
// All entries are addressed relative to its anchored descriptor.
type Store struct {
	pendingMode *runtimeModeProvisionV1
	root        *os.Root
	readOnly    bool
	execution   bool // native execution metadata purpose; never a wire grant
	lockInfo    os.FileInfo
	gate        PublicationGate
	domain      cachefs.Domain
	// Private fault hooks let package tests witness transaction boundaries.
	beforePartSync    func(*os.File) error
	afterLink         func()
	beforePruneRemove func(string)
	onLockBlocked     func()
}

// Open opens or creates a private runtime cache. It refuses symlink ancestors
// and directories writable by another account. It never chmods existing paths.
func Open(dir string) (*Store, error) { return openStore(dir, true) }

// OpenReadOnly opens an existing cache without creating directories or locks.
// A missing cache returns an error matching os.ErrNotExist.
func OpenReadOnly(dir string) (*Store, error) { return openStore(dir, false) }

func openStore(dir string, create bool) (*Store, error) {
	return openStoreDomain(context.Background(), dir, create, cachefs.Domain{})
}

func openStoreDomain(ctx context.Context, dir string, create bool, domain cachefs.Domain) (*Store, error) {
	return openStoreDomainCreated(ctx, dir, create, domain, nil)
}

func openStoreDomainCreated(ctx context.Context, dir string, create bool, domain cachefs.Domain, created func(string, os.FileInfo) error) (*Store, error) {
	var root *os.Root
	var err error
	if create && created != nil {
		root, err = cachefs.OpenPrivateRootCreated(dir, created)
	} else {
		root, err = cachefs.OpenPrivateRoot(dir, create)
	}
	if err != nil {
		return nil, fmt.Errorf("open runtime store: %w", err)
	}
	s := &Store{root: root, readOnly: !create, domain: domain}
	if err := s.establishLock(create); err != nil {
		root.Close()
		return nil, err
	}
	if err := s.withLock(ctx, func(file *os.File) error {
		if err := s.checkDomain(ctx, file, create); err != nil {
			return err
		}
		for _, name := range []string{"sha256", "incoming", "runtime"} {
			if err := ctx.Err(); err != nil {
				return err
			}
			var child *os.Root
			if create && created != nil {
				childPath := filepath.Join(dir, name)
				child, err = cachefs.OpenPrivateChildCreated(root, name, func(info os.FileInfo) error {
					return created(childPath, info)
				})
			} else {
				child, err = cachefs.OpenPrivateChild(root, name, create)
			}
			if err != nil {
				return fmt.Errorf("open runtime store child: %w", err)
			}
			if err := child.Close(); err != nil {
				return fmt.Errorf("close runtime store child: %w", err)
			}
		}
		return nil
	}); err != nil {
		root.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the anchored directory. Its active parts must be closed first.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	if s.pendingMode != nil {
		if err := s.pendingMode.Close(); err != nil {
			return err
		}
		s.pendingMode = nil
	}
	return s.root.Close()
}

func (s *Store) openRegular(name string, mode fs.FileMode) (*os.File, error) {
	return s.openRegularFlags(name, mode, os.O_RDONLY)
}

func (s *Store) openRegularFlags(name string, mode fs.FileMode, flags int) (*os.File, error) {
	return openNoFollow(s.root, name, flags, mode)
}

// openItem checks a file while its caller holds the store transaction.
func (s *Store) openItem(item supply.Item) (*os.File, error) {
	return s.openItemContext(context.Background(), item)
}
func (s *Store) openItemContext(ctx context.Context, item supply.Item) (*os.File, error) {
	_, done, err := s.artifactModeGuard(ctx, item)
	if err != nil {
		return nil, err
	}
	defer done()
	return s.openItemUnfencedContext(ctx, item)
}

func (s *Store) openItemUnfencedContext(ctx context.Context, item supply.Item) (*os.File, error) {
	if !supply.IsHex64(item.SHA256) || !IsRuntime(item.Kind) || item.Size <= 0 {
		return nil, fmt.Errorf("runtime item has invalid metadata")
	}
	f, err := s.openRegular("sha256/"+item.SHA256, Mode(item))
	if err != nil {
		return nil, err
	}
	if err := supply.CheckOpenContext(ctx, item, f); err != nil {
		f.Close()
		return nil, err
	}
	if _, err := f.Seek(0, 0); err != nil {
		f.Close()
		return nil, fmt.Errorf("seek checked runtime file: %w", err)
	}
	return f, nil
}

// has treats corrupt owned bytes as missing. Unsafe entries are errors.
func (s *Store) has(item supply.Item) (bool, error) { return s.hasContext(context.Background(), item) }
func (s *Store) hasContext(ctx context.Context, item supply.Item) (bool, error) {
	f, err := s.openItemContext(ctx, item)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		var mismatch *supply.MismatchError
		if errors.As(err, &mismatch) {
			return false, nil
		}
		return false, err
	}
	return true, f.Close()
}

func (s *Store) syncDir(name string) error {
	f, err := s.root.Open(name)
	if err != nil {
		return fmt.Errorf("open runtime folder for sync: %w", err)
	}
	defer f.Close()
	if err := syncDirectory(f); err != nil {
		return fmt.Errorf("sync runtime folder: %w", err)
	}
	return nil
}

// Present reports an owned regular file of the expected size and mode. It is
// a cheap inventory claim. Commit and OpenItem always verify the actual bytes.
func (s *Store) present(item supply.Item) (bool, error) {
	return s.presentContext(context.Background(), item)
}
func (s *Store) presentContext(ctx context.Context, item supply.Item) (bool, error) {
	_, done, err := s.artifactModeGuard(ctx, item)
	if err != nil {
		return false, err
	}
	defer done()
	if !supply.IsHex64(item.SHA256) || !IsRuntime(item.Kind) {
		return false, fmt.Errorf("invalid runtime inventory item")
	}
	f, err := s.openRegular("sha256/"+item.SHA256, Mode(item))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return false, fmt.Errorf("stat runtime inventory: %w", err)
	}
	return st.Size() == item.Size, nil
}

// OpenItem returns an owned manifest-checked descriptor after the store's
// publication transaction has completed.
func (s *Store) OpenItem(ctx context.Context, item supply.Item) (*os.File, error) {
	var f *os.File
	err := retryExecMode(ctx, func() error {
		return s.locked(ctx, func() error { var err error; f, err = s.openItemContext(ctx, item); return err })
	})
	return f, err
}

// Has checks a stable stored object. A corrupt owned regular object is
// missing for delivery purposes and may be repaired by a verified transfer.
func (s *Store) Has(ctx context.Context, item supply.Item) (bool, error) {
	var has bool
	err := retryExecMode(ctx, func() error {
		return s.locked(ctx, func() error { var err error; has, err = s.hasContext(ctx, item); return err })
	})
	return has, err
}

// Present returns a stable metadata-only presence claim during a transfer.
func (s *Store) Present(ctx context.Context, item supply.Item) (bool, error) {
	var has bool
	err := retryExecMode(ctx, func() error {
		return s.locked(ctx, func() error { var err error; has, err = s.presentContext(ctx, item); return err })
	})
	return has, err
}
