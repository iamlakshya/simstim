package runtimecache

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/supply"
)

// JSON escaping expands each wire byte by at most six, plus the record wrapper.
const maxRecordBytes = 6*proto.MaxFrame + 4096

// Record keeps the exact client-authorised runtime entries and arrival time.
type Record struct {
	Manifest      supply.Manifest `json:"manifest"`
	InstalledAt   time.Time       `json:"installed_at"`
	ImageDefaults *[]supply.Item  `json:"image_defaults,omitempty"`
	extra         map[string]json.RawMessage
}

// Commit is a legacy fixture and migration adapter with unknown image
// authority. Production host publication must use explicit image defaults.
// It must never publish unknown metadata concurrently with image collection.
func (s *Store) Commit(ctx context.Context, manifest supply.Manifest, arch string) error {
	if !s.domain.IsZero() {
		return runtimeDomainError("a host runtime label requires explicit client image-default authority")
	}
	return s.CommitWithImageDefaults(ctx, manifest, nil, arch)
}

// CommitWithImageDefaults stores client-authorised image retention metadata
// beside fully checked runtime blobs. If image GC is enabled, the caller must
// hold shared leases for all referenced platform digests before this call.
// The lock order is image leases before the runtime store transaction.
func (s *Store) CommitWithImageDefaults(ctx context.Context, manifest supply.Manifest, defaults *[]supply.Item, arch string) error {
	if s.readOnly {
		return fmt.Errorf("cannot commit to a read-only runtime store")
	}
	if !s.domain.IsZero() && defaults == nil {
		return runtimeDomainError("a host runtime label has absent client image-default metadata")
	}
	if err := ValidateWithImageDefaults(manifest, defaults, arch); err != nil {
		return err
	}
	return retryExecMode(ctx, func() error {
		return s.withPublication(ctx, func() error {
			return s.locked(ctx, func() error {
				for _, it := range manifest.Items {
					has, err := s.hasContext(ctx, it)
					if err != nil {
						return err
					}
					if !has {
						return fmt.Errorf("runtime is missing %s", it.Name)
					}
					if err := s.installVerityLocked(ctx, it); err != nil {
						return err
					}
				}
				// Recheck real artifact-directory durability before either an
				// existing-label ACK or a new reference can commit. A failed
				// Part close/sync record is never a durability permission.
				if err := s.syncDir("sha256"); err != nil {
					return err
				}
				name := "runtime/" + manifest.Runtime + ".json"
				record := Record{Manifest: manifest, InstalledAt: time.Now().UTC(), ImageDefaults: cloneImageDefaults(defaults)}
				if old, err := s.readRecord(name, arch); err == nil {
					if !reflect.DeepEqual(old.Manifest.Items, manifest.Items) {
						return fmt.Errorf("runtime label already names different entries")
					}
					// Legacy fixture/migration callers cannot erase known authority.
					if defaults == nil {
						return nil
					}
					if old.Manifest.Release != manifest.Release {
						return fmt.Errorf("runtime image metadata names a different release")
					}
					if old.ImageDefaults != nil {
						if !reflect.DeepEqual(*old.ImageDefaults, *defaults) {
							return fmt.Errorf("runtime label already names different image defaults")
						}
						return nil
					}
					record = old // Preserve arrival time, runtime entries and unknown fields.
					record.ImageDefaults = cloneImageDefaults(defaults)
				} else if !errors.Is(err, os.ErrNotExist) {
					return err
				}
				data, err := json.Marshal(record)
				if err != nil {
					return fmt.Errorf("encode runtime record: %w", err)
				}
				var nonce [16]byte
				if _, err := rand.Read(nonce[:]); err != nil {
					return fmt.Errorf("name runtime record: %w", err)
				}
				tmp := "runtime/.record-" + hex.EncodeToString(nonce[:])
				f, err := openNoFollow(s.root, tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
				if err != nil {
					return fmt.Errorf("create runtime record: %w", err)
				}
				defer func() {
					current, err := s.root.Lstat(tmp)
					own, ferr := f.Stat()
					if err == nil && ferr == nil && os.SameFile(current, own) {
						_ = s.root.Remove(tmp)
					}
					_ = f.Close()
				}()
				if _, err := f.Write(append(data, '\n')); err != nil {
					return fmt.Errorf("write runtime record: %w", err)
				}
				if err := f.Sync(); err != nil {
					return fmt.Errorf("sync runtime record: %w", err)
				}
				current, err := s.root.Lstat(tmp)
				own, ferr := f.Stat()
				if err != nil || ferr != nil || !os.SameFile(current, own) {
					return fmt.Errorf("runtime record changed before publication")
				}
				if err := s.root.Rename(tmp, name); err != nil {
					return fmt.Errorf("publish runtime record: %w", err)
				}
				return s.syncDir("runtime")
			})
		})
	})
}

func (s *Store) readRecord(name, arch string) (Record, error) {
	var record Record
	f, err := s.openRegular(name, 0o600)
	if err != nil {
		return record, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return record, fmt.Errorf("stat runtime record: %w", err)
	}
	if st.Size() > maxRecordBytes {
		return record, fmt.Errorf("runtime record is too large")
	}
	dec := json.NewDecoder(io.LimitReader(f, maxRecordBytes+1))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&record); err != nil {
		return record, fmt.Errorf("read runtime record: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return record, fmt.Errorf("runtime record has trailing content")
	}
	if err := ValidateWithImageDefaults(record.Manifest, record.ImageDefaults, arch); err != nil {
		return record, err
	}
	if name != "runtime/"+record.Manifest.Runtime+".json" || record.InstalledAt.IsZero() {
		return record, fmt.Errorf("runtime record identity is invalid")
	}
	return record, nil
}

// Records returns every safe runtime record. Any unreadable record fails
// closed so retention cannot discard the files it may name.
func (s *Store) Records(arch string) ([]Record, error) {
	return s.RecordsContext(context.Background(), arch)
}

// RecordsContext checks the domain and reads all records under the canonical
// store transaction. Callers already in that transaction use recordsContext.
func (s *Store) RecordsContext(ctx context.Context, arch string) ([]Record, error) {
	var records []Record
	err := s.locked(ctx, func() error {
		var err error
		records, err = s.recordsContext(ctx, arch)
		return err
	})
	return records, err
}

func (s *Store) recordsContext(ctx context.Context, arch string) ([]Record, error) {
	dir, err := s.root.Open("runtime")
	if err != nil {
		return nil, fmt.Errorf("open runtime records: %w", err)
	}
	defer dir.Close()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return nil, fmt.Errorf("list runtime records: %w", err)
	}
	var records []Record
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if strings.HasPrefix(e.Name(), ".record-") {
			continue
		}
		if !strings.HasSuffix(e.Name(), ".json") {
			return nil, fmt.Errorf("runtime records contain an unknown entry")
		}
		record, err := s.readRecord("runtime/"+e.Name(), arch)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].InstalledAt.Equal(records[j].InstalledAt) {
			return records[i].Manifest.Runtime > records[j].Manifest.Runtime
		}
		return records[i].InstalledAt.After(records[j].InstalledAt)
	})
	return records, nil
}
