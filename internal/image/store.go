package image

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/proto"
)

// Store anchors the private image store for all of its operations.
type Store struct {
	root         *os.Root
	gate         PublicationGate
	domain       cachefs.Domain
	existingOnly bool
	generation   cachefs.Reference
	receipts     bool
	captureHook  func(bool, *os.Root, string, string)
}

// Open opens an owned private store without following symbolic links.
func Open(dir string) (*Store, error) { return OpenWithPublicationGate(dir, nil) }

// Close releases the store descriptor after operations have finished.
func (s *Store) Close() error { return s.root.Close() }

// Lease keeps a platform image stable until a trusted manifest records it.
type Lease struct{ file *os.File }

// Close releases an image lock.
func (l *Lease) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	return l.file.Close()
}

var errImageLeaseBusy = errors.New("the existing image resource lease is busy")

func (s *Store) lock(ctx context.Context, name string, shared bool) (*Lease, error) {
	return s.takeLease(ctx, name, shared, true, false)
}

func (s *Store) existingLease(ctx context.Context, name string, shared bool) (*Lease, error) {
	return s.takeLease(ctx, name, shared, false, false)
}

func (s *Store) tryExistingLease(ctx context.Context, name string, shared bool) (*Lease, bool, error) {
	lease, err := s.takeLease(ctx, name, shared, false, true)
	if errors.Is(err, errImageLeaseBusy) {
		return nil, false, nil
	}
	return lease, err == nil, err
}

func (s *Store) shareLease(ctx context.Context, name string, lease *Lease) (*Lease, error) {
	file := lease.file
	lease.file = nil
	return s.waitLease(ctx, name, file, true, nil, false)
}

func (s *Store) takeLease(ctx context.Context, name string, shared, create, once bool) (*Lease, error) {
	if _, valid := leaseRole(name); !valid {
		return nil, ownershipError("lease path has no closed resource role")
	}
	f, err := s.regular(name, os.O_RDWR, 0o600)
	if errors.Is(err, os.ErrNotExist) && create && !s.existingOnly {
		err = s.withPublication(ctx, func() error {
			if err := s.permitLeaseCreation(ctx, name); err != nil {
				return err
			}
			var openErr error
			f, openErr = s.regular(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
			created := openErr == nil
			if errors.Is(openErr, os.ErrExist) {
				f, openErr = s.regular(name, os.O_RDWR, 0o600)
			}
			if openErr == nil && created && s.receipts {
				if openErr = f.Sync(); openErr == nil {
					openErr = s.syncDir(filepath.Dir(name))
				}
				if openErr == nil {
					openErr = s.recordLease(name, f)
				}
			}
			return openErr
		})
	}
	if err != nil {
		if f != nil {
			_ = f.Close()
		}
		return nil, err
	}
	return s.waitLease(ctx, name, f, shared, nil, once)
}

func (s *Store) waitLease(ctx context.Context, name string, f *os.File, shared bool, captured *leaseOwnership, once bool) (*Lease, error) {
	for {
		if err := ctx.Err(); err != nil {
			f.Close()
			return nil, err
		}
		mode := cachefs.Exclusive
		if shared {
			mode = cachefs.Shared
		}
		ok, err := cachefs.TryLock(f, mode)
		if err != nil {
			f.Close()
			return nil, err
		}
		if ok {
			root, leaf, err := s.parent(name)
			if err != nil {
				f.Close()
				return nil, err
			}
			_, err = cachefs.ValidateFile(root, leaf, f, 0o600)
			if err == nil && captured != nil {
				err = captured.Parent.CheckRoot(root)
				if err == nil {
					err = captured.File.CheckFile(root, leaf, f, 0o600)
				}
			}
			root.Close()
			if err == nil && captured == nil {
				err = s.checkLeaseOwner(ctx, name, f)
			}
			if err != nil {
				f.Close()
				return nil, err
			}
			return &Lease{f}, nil
		}
		if once {
			f.Close()
			return nil, errImageLeaseBusy
		}
		if err := waitImageLeaseRetry(ctx); err != nil {
			f.Close()
			return nil, err
		}
	}
}

func waitImageLeaseRetry(ctx context.Context) error {
	timer := time.NewTimer(20 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Acquire holds the platform's shared lock before a start records its image.
func (s *Store) Acquire(ctx context.Context, digest string) (*Lease, error) {
	if !ValidDigest(digest) {
		return nil, badLayer("invalid platform digest")
	}
	return s.lock(ctx, digest[7:]+".lock", true)
}

func (s *Store) readRecord(hex string) (Record, error) {
	var record Record
	if !s.domain.IsZero() {
		receipt, err := s.readOwnership("sha256:" + hex)
		if err != nil {
			return record, err
		}
		if receipt.Leaf != hex || receipt.Pending != nil || receipt.Purge != nil {
			return record, ownershipError("host image is not a stable owned publication")
		}
		root, err := s.ownedDirectory(receipt, hex, false)
		if err != nil {
			return record, err
		}
		root.Close()
	}
	f, err := s.regular(hex+"/image.json", os.O_RDONLY, 0o600)
	if err != nil {
		return record, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxManifest+1))
	if err != nil {
		return record, err
	}
	if int64(len(data)) > MaxManifest {
		return record, badLayer("image metadata exceeds 4 MiB")
	}
	if err := json.Unmarshal(data, &record); err != nil {
		return record, err
	}
	if record.PlatformDigest != "sha256:"+hex || !ValidDigest(record.Digest) || record.Size <= 0 || record.RootHash == "" {
		return record, badLayer("invalid stored image metadata")
	}
	file, err := s.regular(hex+"/rootfs.img", os.O_RDONLY, 0o444)
	if err != nil {
		return record, err
	}
	defer file.Close()
	info, err := Inspect(file)
	if err != nil {
		return record, err
	}
	if info.RootHash != record.RootHash || info.DataBlocks != record.DataBlocks || info.Size != record.Size {
		return record, mismatch("stored image")
	}
	return record, nil
}

// List reads host inventory. It never supplies authority to the client.
func (s *Store) List(ctx context.Context) ([]Record, error) {
	dir, err := s.root.Open(".")
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	records := []Record{}
	for _, entry := range entries {
		if !ValidDigest("sha256:" + entry.Name()) {
			continue
		}
		lease, err := s.lock(ctx, entry.Name()+".lock", true)
		if err != nil {
			return nil, err
		}
		record, readErr := s.readRecord(entry.Name())
		lease.Close()
		if errors.Is(readErr, os.ErrNotExist) {
			continue
		}
		if readErr != nil {
			return nil, readErr
		}
		records = append(records, record)
	}
	return records, nil
}

// References is read again while an image's exclusive lock is held.
type References func() (map[string]string, error)

// Remove refuses every trusted session reference, including stopped sessions.
func (s *Store) Remove(ctx context.Context, digest string, refs References) error {
	if !ValidDigest(digest) {
		return badLayer("invalid image digest")
	}
	lease, err := s.lock(ctx, digest[7:]+".lock", false)
	if err != nil {
		return err
	}
	defer lease.Close()
	used, err := refs()
	if err != nil {
		return err
	}
	if session := used[digest]; session != "" {
		return &proto.Error{Code: "image_in_use", Message: fmt.Sprintf("session %s still uses image %s", session, digest), Hint: "Remove that session, then remove the image."}
	}
	_, err = s.readRecord(digest[7:])
	if err != nil {
		return err
	}
	if !s.domain.IsZero() {
		return s.withPublication(ctx, func() error { return s.purgeImageLocked(ctx, digest, &PurgeReport{}) })
	}
	return s.root.RemoveAll(digest[7:])
}

// Prune keeps references, release defaults, recent use and recent arrival.
func (s *Store) Prune(ctx context.Context, refs References, defaults func() (map[string]bool, error), now time.Time) ([]string, error) {
	records, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	removed := []string{}
	for _, record := range records {
		lease, err := s.lock(ctx, record.PlatformDigest[7:]+".lock", false)
		if err != nil {
			return removed, err
		}
		fresh, readErr := s.readRecord(record.PlatformDigest[7:])
		if errors.Is(readErr, os.ErrNotExist) {
			lease.Close()
			continue
		}
		if readErr != nil {
			lease.Close()
			return removed, readErr
		}
		used, refErr := refs()
		if refErr != nil {
			lease.Close()
			return removed, refErr
		}
		pins, err := defaults()
		if err != nil {
			lease.Close()
			return removed, err
		}
		keep := used[fresh.PlatformDigest] != "" || pins[fresh.Digest] || pins[fresh.PlatformDigest] || now.Sub(fresh.ConvertedAt) < 7*24*time.Hour || now.Sub(fresh.UsedAt) < 7*24*time.Hour || now.Sub(fresh.ConvertedAt) < time.Hour
		if !keep {
			if s.domain.IsZero() {
				err = s.root.RemoveAll(fresh.PlatformDigest[7:])
			} else {
				err = s.withPublication(ctx, func() error { return s.purgeImageLocked(ctx, fresh.PlatformDigest, &PurgeReport{}) })
			}
			if err == nil {
				removed = append(removed, fresh.PlatformDigest)
			}
		}
		lease.Close()
		if err != nil {
			return removed, err
		}
	}
	return removed, nil
}
