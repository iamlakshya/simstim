package client

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"time"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

// placementStore owns only the two local advisory placement files. Its root
// and original Settings source cannot supply host or native-start authority.
type placementStore struct {
	operation *settingsOperation
	root      *os.Root
	reference cachefs.Reference
}

var errPlacementState = errors.New("the local placement state is not a supported complete record")

func openPlacementStore(ctx context.Context, operation *settingsOperation) (*placementStore, error) {
	metadata, err := newOrdinaryLocalGitMetadataSource(ctx, operation)
	if err != nil {
		return nil, err
	}
	check := metadata.CheckKeys(ctx, []string{"XDG_CACHE_HOME", "HOME", "USERPROFILE"})
	if err := errors.Join(check, metadata.Close()); err != nil {
		return nil, err
	}
	root, err := cachefs.OpenPrivateRoot(CacheDir(), true)
	if err != nil {
		return nil, placementStoreError(err)
	}
	reference, err := cachefs.CaptureRoot(root)
	if err != nil {
		return nil, errors.Join(placementStoreError(err), root.Close())
	}
	store := &placementStore{operation: operation, root: root, reference: reference}
	if err := store.check(ctx); err != nil {
		return nil, errors.Join(err, root.Close())
	}
	return store, nil
}

func placementStoreError(err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return &UserError{Msg: "the local placement cache is unavailable or changed",
		Hint: "Check the private cache folder and retry placement.", Code: "placement_cache", Kind: KindFailed}
}

func (s *placementStore) check(ctx context.Context) error {
	if s == nil || s.root == nil || s.operation == nil {
		return placementStoreError(os.ErrClosed)
	}
	if err := s.operation.Revalidate(ctx); err != nil {
		return err
	}
	if err := s.reference.CheckRoot(s.root); err != nil {
		return placementStoreError(err)
	}
	current, err := cachefs.OpenPrivateRoot(CacheDir(), false)
	if err != nil {
		return placementStoreError(err)
	}
	defer current.Close()
	return placementStoreError(s.reference.CheckRoot(current))
}

func (s *placementStore) close() error {
	if s == nil || s.root == nil {
		return nil
	}
	err := s.root.Close()
	s.root = nil
	return placementStoreError(err)
}

func (s *placementStore) locked(ctx context.Context, name string, visit func(context.Context) error) (failure error) {
	if name != "facts.json" && name != "placements.json" || visit == nil {
		return placementStoreError(os.ErrInvalid)
	}
	if err := s.check(ctx); err != nil {
		return err
	}
	lock, err := cachefs.OpenFile(s.root, name+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return placementStoreError(err)
	}
	defer func() { failure = errors.Join(failure, placementStoreError(lock.Close())) }()
	reference, err := cachefs.CaptureFile(s.root, name+".lock", lock, 0600)
	if err != nil {
		return placementStoreError(err)
	}
	wait, stop := context.WithTimeout(ctx, hostsWait)
	defer stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		if err := wait.Err(); err != nil {
			return err
		}
		taken, err := cachefs.TryLock(lock, cachefs.Exclusive)
		if err != nil {
			return placementStoreError(err)
		}
		if taken {
			break
		}
		select {
		case <-wait.Done():
			return wait.Err()
		case <-tick.C:
		}
	}
	if err := reference.CheckFile(s.root, name+".lock", lock, 0600); err != nil {
		return placementStoreError(err)
	}
	if err := s.check(ctx); err != nil {
		return err
	}
	if err := visit(ctx); err != nil {
		return err
	}
	if err := reference.CheckFile(s.root, name+".lock", lock, 0600); err != nil {
		return placementStoreError(err)
	}
	return s.check(ctx)
}

type placementContextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r placementContextReader) Read(out []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.r.Read(out)
	if stopped := r.ctx.Err(); stopped != nil {
		return n, stopped
	}
	return n, err
}

func (s *placementStore) read(ctx context.Context, name string) ([]byte, bool, error) {
	if name != "facts.json" && name != "placements.json" {
		return nil, false, placementStoreError(errPlacementState)
	}
	file, err := cachefs.OpenFile(s.root, name, os.O_RDONLY, 0600)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, placementStoreError(err)
	}
	defer file.Close()
	reference, err := cachefs.CaptureFile(s.root, name, file, 0600)
	if err != nil {
		return nil, false, placementStoreError(err)
	}
	raw, err := io.ReadAll(placementContextReader{ctx, file})
	if err != nil {
		return nil, false, placementStoreError(err)
	}
	if err := reference.CheckFile(s.root, name, file, 0600); err != nil {
		return nil, false, placementStoreError(err)
	}
	return raw, true, s.check(ctx)
}

func (s *placementStore) write(ctx context.Context, name string, value any) (failure error) {
	if name != "facts.json" && name != "placements.json" {
		return placementStoreError(errPlacementState)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return placementStoreError(err)
	}
	if err := s.check(ctx); err != nil {
		return err
	}
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return placementStoreError(err)
	}
	temporary := "." + name + "-" + hex.EncodeToString(nonce[:])
	file, err := cachefs.OpenFile(s.root, temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return placementStoreError(err)
	}
	reference, err := cachefs.CaptureFile(s.root, temporary, file, 0600)
	if err != nil {
		_ = file.Close()
		return placementStoreError(err)
	}
	published := false
	defer func() {
		if !published && reference.CheckCreatedFile(s.root, temporary, file, 0600) == nil {
			failure = errors.Join(failure, placementStoreError(s.root.Remove(temporary)))
		}
		failure = errors.Join(failure, placementStoreError(file.Close()))
	}()
	if err := writeLedgerBytes(file, encoded); err != nil {
		return placementStoreError(err)
	}
	if err := file.Sync(); err != nil {
		return placementStoreError(err)
	}
	if err := s.check(ctx); err != nil {
		return err
	}
	if err := reference.CheckCreatedFile(s.root, temporary, file, 0600); err != nil {
		return placementStoreError(err)
	}
	if err := s.root.Rename(temporary, name); err != nil {
		return placementStoreError(err)
	}
	published = true
	directory, err := s.root.Open(".")
	if err != nil {
		return placementStoreError(err)
	}
	flush := cachefs.SyncDirectory(directory)
	closed := directory.Close()
	if err := errors.Join(flush, closed); err != nil {
		return placementStoreError(err)
	}
	return s.check(context.WithoutCancel(ctx))
}
