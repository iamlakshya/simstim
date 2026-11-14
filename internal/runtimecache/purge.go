package runtimecache

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/supply"
)

// PurgeEntry is one canonically checked owned object under the anchored store.
// Item is present only for a content-addressed blob. Info pins the opened inode.
// A callback must durably journal and recheck this reference before removal.
type PurgeEntry struct {
	Name string
	Item *supply.Item
	Info os.FileInfo
}

// OpenForPurge opens only an existing root and its existing stable sentinel.
// Already-removed empty children are allowed during an interrupted purge.
// This constructor never creates a namespace, child or replacement sentinel.
func OpenForPurge(dir string) (*Store, error) {
	return openPurgeStore(context.Background(), dir, cachefs.Domain{})
}

func openPurgeStore(ctx context.Context, dir string, domain cachefs.Domain) (*Store, error) {
	root, err := cachefs.OpenPrivateRoot(dir, false)
	if err != nil {
		return nil, fmt.Errorf("open existing runtime purge store: %w", err)
	}
	s := &Store{root: root, readOnly: true, domain: domain}
	if err := s.establishLock(false); err != nil {
		root.Close()
		return nil, err
	}
	if err := s.locked(ctx, func() error { return nil }); err != nil {
		root.Close()
		return nil, err
	}
	return s, nil
}

// PurgeOwned nominates record-authorised objects for terminal host removal.
// The host must first drain publishers and wait for every admitted operation.
// References are reread under the existing store transaction. All blobs precede
// all records, retaining crash authority until the last blob callback succeeds.
// Unknown records, orphan objects, incoming parts and other entries are never
// adopted. The callback uses the host's persistent outside cleanup journal.
// The returned sentinel identity may be removed only after Close, all other
// entries are absent, and the external terminal drain still owns the namespace.
func (s *Store) PurgeOwned(ctx context.Context, arch string, readReferences func() ([]References, error), remove func(PurgeEntry) error) (os.FileInfo, error) {
	if readReferences == nil || remove == nil {
		return nil, fmt.Errorf("runtime purge requires references and a journalled remover")
	}
	err := s.locked(ctx, func() error {
		refs, err := readReferences()
		if err != nil {
			return err
		}
		if len(refs) != 0 {
			return fmt.Errorf("runtime purge still has trusted session references")
		}
		records, err := s.recordsContext(ctx, arch)
		if errors.Is(err, os.ErrNotExist) {
			records, err = nil, nil
		}
		if err != nil {
			return err
		}
		items := map[string]supply.Item{}
		for _, record := range records {
			if err := ctx.Err(); err != nil {
				return err
			}
			if len(record.extra) != 0 {
				return fmt.Errorf("runtime purge cannot interpret a future record")
			}
			for _, item := range record.Manifest.Items {
				if old, exists := items[item.SHA256]; exists && (old.Size != item.Size || Mode(old) != Mode(item) || old.RootHash != item.RootHash || old.Salt != item.Salt || old.DataBlocks != item.DataBlocks) {
					return fmt.Errorf("runtime purge records disagree about one object")
				}
				items[item.SHA256] = item
			}
		}
		digests := make([]string, 0, len(items))
		for digest := range items {
			digests = append(digests, digest)
		}
		sort.Strings(digests)
		for _, digest := range digests {
			if err := ctx.Err(); err != nil {
				return err
			}
			item := items[digest]
			file, err := s.openItemContext(ctx, item)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return fmt.Errorf("verify runtime purge object: %w", err)
			}
			info, err := file.Stat()
			if err == nil {
				err = remove(PurgeEntry{Name: "sha256/" + digest, Item: &item, Info: info})
			}
			closeErr := file.Close()
			if err != nil || closeErr != nil {
				return errors.Join(err, closeErr)
			}
		}
		for _, record := range records {
			if err := ctx.Err(); err != nil {
				return err
			}
			name := "runtime/" + record.Manifest.Runtime + ".json"
			file, err := s.openRegular(name, 0o600)
			if err != nil {
				return err
			}
			info, err := file.Stat()
			if err == nil {
				err = remove(PurgeEntry{Name: name, Info: info})
			}
			closeErr := file.Close()
			if err != nil || closeErr != nil {
				return errors.Join(err, closeErr)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.lockInfo, nil
}
