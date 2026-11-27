package checkpoint

import (
	"context"
	"errors"
	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/proto"
	"time"
)

// LocalBackendV1 adapts an actual private-root Store. It grants no native
// source authority beyond the original owner that issued that Store.
type LocalBackendV1 struct {
	store *Store
}

func (backend *LocalBackendV1) CheckSpaceV1(ctx context.Context, needed int64) error {
	if backend == nil || backend.store == nil || needed <= 0 {
		return failure("the checkpoint space check has no actual disk budget", nil)
	}
	s := backend.store
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkOwned(ctx, s.check); err != nil {
		return err
	}
	space, err := files.SpaceAt(s.session)
	if err != nil {
		return failure("cannot measure the checkpoint filesystem", err)
	}
	if space.Available < uint64(needed) {
		return &proto.Error{Code: "checkpoint_low_disk", Message: "checkpoints paused: low disk", Hint: "Free at least sandbox.disk_max on the session filesystem."}
	}
	return s.checkOwned(ctx, s.check)
}

// NewLocalBackendV1 wraps an already issued private-root Store. Slot roots
// remain with their own native backend and are never accepted by this adapter.
func NewLocalBackendV1(store *Store) *LocalBackendV1 {
	return &LocalBackendV1{store: store}
}

// Probe tests the actual private-root producer's clone operation.
func (backend *LocalBackendV1) Probe(ctx context.Context) error {
	if backend == nil {
		return failure("the original local checkpoint backend is absent", nil)
	}
	return backend.store.Probe(ctx)
}

// List returns the actual private-root producer's complete checkpoints.
func (backend *LocalBackendV1) List(ctx context.Context) ([]Entry, error) {
	if backend == nil {
		return nil, failure("the original local checkpoint backend is absent", nil)
	}
	return backend.store.List(ctx)
}

// Create copies the current disk through the actual private-root producer.
func (backend *LocalBackendV1) Create(ctx context.Context, at time.Time, metadata Metadata, keep int) (Entry, error) {
	if backend == nil {
		return Entry{}, failure("the original local checkpoint backend is absent", nil)
	}
	return backend.store.Create(ctx, at, metadata, keep)
}

// Trim applies retention through the actual private-root producer.
func (backend *LocalBackendV1) Trim(ctx context.Context, keep int) error {
	if backend == nil {
		return failure("the original local checkpoint backend is absent", nil)
	}
	return backend.store.Trim(ctx, keep)
}

func (backend *LocalBackendV1) DiscardCreatedV1(ctx context.Context, entry Entry) error {
	if backend == nil || backend.store == nil {
		return failure("the original checkpoint creator is absent", nil)
	}
	return backend.store.RemoveCreated(ctx, entry)
}

// Select pins one fixed timestamp without retaining a running disk borrow.
// Its independent read-only roots and files survive this Store closing.
func (backend *LocalBackendV1) Select(ctx context.Context, name string) (SnapshotV1, error) {
	if backend == nil || backend.store == nil {
		return nil, failure("the original local checkpoint backend is absent", nil)
	}
	at, err := time.Parse(TimeLayout, name)
	if err != nil || at.Format(TimeLayout) != name {
		return nil, failure("the checkpoint selection is not a fixed UTC timestamp", err)
	}
	s := backend.store
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkOwned(ctx, s.check); err != nil {
		return nil, err
	}
	snapshot, err := retainLocalSnapshotV1(s.session, s.entries, name, nil)
	if err != nil {
		return nil, err
	}
	if err := s.checkOwned(ctx, s.check); err != nil {
		return nil, errors.Join(err, snapshot.Close())
	}
	return snapshot, nil
}

// PrepareRestore retains a private snapshot from the SAME protected session
// and roots, including one selected before its running Store closed. The
// supplied stopped check belongs to the host's private actual native issuer.
func (backend *LocalBackendV1) PrepareRestore(ctx context.Context, snapshot SnapshotV1, transaction TransactionV1, checkStopped func(context.Context) error) (PreparedRestoreV1, error) {
	if backend == nil || backend.store == nil || checkStopped == nil {
		return nil, failure("rewind has no original local backend and stopped owner", nil)
	}
	selected, ok := snapshot.(*localSnapshotV1)
	if !ok || selected == nil {
		return nil, failure("the snapshot was not issued by an original local producer", nil)
	}
	return backend.store.prepareRestoreV1(ctx, selected, transaction, checkStopped)
}

func timeFromKeyV1(key string) (time.Time, error) {
	at, err := time.Parse(TimeLayout, key)
	if err != nil || at.Format(TimeLayout) != key {
		return time.Time{}, failure("the checkpoint name is not a fixed UTC timestamp", err)
	}
	return at, nil
}

var _ BackendV1 = (*LocalBackendV1)(nil)
