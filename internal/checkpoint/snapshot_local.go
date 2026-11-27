package checkpoint

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

// This private marker is issued only while Select holds a checked original
// Store. Neither persisted references nor a copied Entry can construct one.
type localSnapshotOriginV1 struct {
	session cachefs.Reference
	entries cachefs.Reference
}

func (snapshot *localSnapshotV1) DiskBytesV1() int64 {
	if snapshot == nil {
		return 0
	}
	snapshot.mu.Lock()
	defer snapshot.mu.Unlock()
	if snapshot.closed {
		return 0
	}
	return snapshot.diskRef.Size
}

func (snapshot *localSnapshotV1) CheckV1(ctx context.Context) error {
	if snapshot == nil || ctx == nil {
		return failure("the original read-only snapshot is absent", nil)
	}
	snapshot.mu.Lock()
	defer snapshot.mu.Unlock()
	return errors.Join(ctx.Err(), snapshot.checkReadV1())
}

type localSnapshotV1 struct {
	mu                     sync.Mutex
	session, entries       *os.Root
	sessionRef, entriesRef cachefs.Reference
	disk, metadata         *os.File
	diskRef, metadataRef   cachefs.Reference
	origin                 *localSnapshotOriginV1
	key                    string
	entry                  Entry
	closed                 bool
	closeErr               error
}

func (snapshot *localSnapshotV1) Entry() Entry {
	if snapshot == nil {
		return Entry{}
	}
	snapshot.mu.Lock()
	defer snapshot.mu.Unlock()
	return Entry{Time: snapshot.entry.Time, Metadata: cloneMetadata(snapshot.entry.Metadata)}
}

func (snapshot *localSnapshotV1) Close() error {
	if snapshot == nil {
		return nil
	}
	snapshot.mu.Lock()
	defer snapshot.mu.Unlock()
	if snapshot.closed {
		return snapshot.closeErr
	}
	snapshot.closed = true
	var result error
	if snapshot.disk != nil {
		result = errors.Join(result, snapshot.disk.Close())
	}
	if snapshot.metadata != nil {
		result = errors.Join(result, snapshot.metadata.Close())
	}
	if snapshot.entries != nil {
		result = errors.Join(result, snapshot.entries.Close())
	}
	if snapshot.session != nil {
		result = errors.Join(result, snapshot.session.Close())
	}
	snapshot.closeErr = result
	return snapshot.closeErr
}

func retainLocalSnapshotV1(session, entries *os.Root, key string, original *localSnapshotV1) (_ *localSnapshotV1, returned error) {
	snapshot := &localSnapshotV1{key: key}
	complete := false
	defer func() {
		if !complete {
			returned = errors.Join(returned, snapshot.Close())
		}
	}()
	var err error
	snapshot.session, err = session.OpenRoot(".")
	if err != nil {
		return nil, failure("cannot retain the selected checkpoint's original session root", err)
	}
	snapshot.sessionRef, err = cachefs.CaptureRoot(snapshot.session)
	if err != nil {
		return nil, err
	}
	snapshot.entries, err = entries.OpenRoot(".")
	if err != nil {
		return nil, failure("cannot retain the selected checkpoint's original read root", err)
	}
	snapshot.entriesRef, err = cachefs.CaptureRoot(snapshot.entries)
	if err != nil {
		return nil, err
	}
	if err := checkNamedRoot(snapshot.session, "checkpoints", snapshot.entries); err != nil {
		return nil, err
	}
	snapshot.disk, err = cachefs.OpenFile(snapshot.entries, key+".disk", os.O_RDONLY, 0o600)
	if err != nil {
		return nil, err
	}
	snapshot.metadata, err = cachefs.OpenFile(snapshot.entries, key+".json", os.O_RDONLY, 0o600)
	if err != nil {
		return nil, err
	}
	snapshot.diskRef, err = cachefs.CaptureFile(snapshot.entries, key+".disk", snapshot.disk, 0o600)
	if err != nil {
		return nil, err
	}
	snapshot.metadataRef, err = cachefs.CaptureFile(snapshot.entries, key+".json", snapshot.metadata, 0o600)
	if err != nil {
		return nil, err
	}
	if original != nil {
		if original.origin == nil || original.closed || original.key != key {
			return nil, failure("the original read-only snapshot issuance ended", nil)
		}
		if err := errors.Join(original.sessionRef.CheckRoot(snapshot.session), original.entriesRef.CheckRoot(snapshot.entries), original.diskRef.CheckFile(snapshot.entries, key+".disk", snapshot.disk, 0o600), original.metadataRef.CheckFile(snapshot.entries, key+".json", snapshot.metadata, 0o600)); err != nil {
			return nil, err
		}
		snapshot.origin = original.origin
		snapshot.entry = Entry{Time: original.entry.Time, Metadata: cloneMetadata(original.entry.Metadata)}
	} else {
		data, err := io.ReadAll(io.LimitReader(snapshot.metadata, maxMetadata+1))
		if err != nil {
			return nil, failure("cannot read the retained checkpoint metadata", err)
		}
		metadata, err := decodeMetadata(data)
		if err != nil {
			return nil, failure("cannot decode the retained checkpoint metadata", err)
		}
		snapshot.entry.Metadata = metadata
		snapshot.entry.Time, err = timeFromKeyV1(key)
		if err != nil {
			return nil, err
		}
		snapshot.origin = &localSnapshotOriginV1{session: snapshot.sessionRef, entries: snapshot.entriesRef}
	}
	if err := snapshot.checkReadV1(); err != nil {
		return nil, err
	}
	complete = true
	return snapshot, nil
}

func (snapshot *localSnapshotV1) checkReadV1() error {
	if snapshot == nil || snapshot.closed || snapshot.origin == nil {
		return failure("the original read-only checkpoint snapshot is unavailable", nil)
	}
	return errors.Join(snapshot.origin.session.CheckRoot(snapshot.session), snapshot.origin.entries.CheckRoot(snapshot.entries), snapshot.sessionRef.CheckRoot(snapshot.session), snapshot.entriesRef.CheckRoot(snapshot.entries), checkNamedRoot(snapshot.session, "checkpoints", snapshot.entries), snapshot.diskRef.CheckFile(snapshot.entries, snapshot.key+".disk", snapshot.disk, 0o600), snapshot.metadataRef.CheckFile(snapshot.entries, snapshot.key+".json", snapshot.metadata, 0o600))
}

func (snapshot *localSnapshotV1) checkAgainstV1(s *Store) error {
	if s == nil {
		return failure("the stopped checkpoint backend has no original store", nil)
	}
	if err := snapshot.checkReadV1(); err != nil {
		return err
	}
	return errors.Join(snapshot.sessionRef.CheckRoot(s.session), snapshot.entriesRef.CheckRoot(s.entries), snapshot.diskRef.CheckFile(s.entries, snapshot.key+".disk", snapshot.disk, 0o600), snapshot.metadataRef.CheckFile(s.entries, snapshot.key+".json", snapshot.metadata, 0o600))
}

var _ SnapshotV1 = (*localSnapshotV1)(nil)
