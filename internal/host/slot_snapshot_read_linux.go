//go:build linux

package host

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"sync"

	"github.com/iamlakshya/simstim/internal/checkpoint"
	"github.com/iamlakshya/simstim/internal/slotlaunch"
)

// This read class can outlive the running owner and VM. It never grants
// current-disk mutation, a running Source or a stopped reservation.
type slotSnapshotReadV1 struct {
	mu                        sync.Mutex
	operations                sync.Mutex
	active                    sync.WaitGroup
	issuer                    *slotSnapshotReadV1
	origin                    *SlotSnapshotOwnerV1
	lifetime                  context.Context
	cancel                    context.CancelFunc
	roles                     []*os.File
	disk, metadata            *os.File
	diskInfo, metadataInfo    os.FileInfo
	entry                     checkpoint.Entry
	pending                   []*slotlaunch.CheckpointOperationOwnerV1
	admitted, closing, closed bool
	closeErr                  error
}

func (selected *slotSnapshotReadV1) beginV1(ctx context.Context) (func(), error) {
	if selected == nil || ctx == nil {
		return nil, rewindFailureV1("the selected Snapshot has no original read owner")
	}
	selected.mu.Lock()
	defer selected.mu.Unlock()
	if selected.issuer != selected || selected.origin == nil || !selected.admitted || selected.closing || selected.closed || selected.lifetime == nil || selected.lifetime.Err() != nil || ctx.Err() != nil {
		return nil, rewindFailureV1("the actual selected Snapshot read owner ended")
	}
	selected.active.Add(1)
	return selected.active.Done, nil
}

func (selected *slotSnapshotReadV1) DiskBytesV1() int64 {
	if selected == nil {
		return 0
	}
	selected.mu.Lock()
	defer selected.mu.Unlock()
	if selected.issuer != selected || !selected.admitted || selected.diskInfo == nil {
		return 0
	}
	return selected.diskInfo.Size()
}

func (selected *slotSnapshotReadV1) Entry() checkpoint.Entry {
	if selected == nil {
		return checkpoint.Entry{}
	}
	selected.mu.Lock()
	defer selected.mu.Unlock()
	if selected.issuer != selected || !selected.admitted {
		return checkpoint.Entry{}
	}
	raw, err := checkpoint.EncodeMetadataV1(selected.entry.Metadata)
	if err != nil {
		return checkpoint.Entry{}
	}
	copy, err := checkpoint.DecodeMetadataV1(raw)
	if err != nil {
		return checkpoint.Entry{}
	}
	return checkpoint.Entry{Time: selected.entry.Time, Metadata: copy}
}

func (selected *slotSnapshotReadV1) CheckV1(ctx context.Context) error {
	done, err := selected.beginV1(ctx)
	if err != nil {
		return err
	}
	defer done()
	selected.operations.Lock()
	defer selected.operations.Unlock()
	if err := selected.checkReadFilesV1(ctx); err != nil {
		return err
	}
	borrowed, cancel := context.WithCancel(ctx)
	joined := make(chan struct{})
	stop := context.AfterFunc(selected.lifetime, func() { defer close(joined); cancel() })
	defer func() {
		cancel()
		if !stop() {
			<-joined
		}
	}()
	roles := append(append([]*os.File(nil), selected.roles...), selected.disk, selected.metadata)
	operation, err := slotlaunch.RunCheckpointOperationV1(borrowed, roles, slotlaunch.CheckpointOperationV1{Session: selected.origin.operation.Primary(), Action: "check", Name: selected.entry.Time.Format(checkpoint.TimeLayout)})
	if operation != nil {
		selected.pending = append(selected.pending, operation)
		closeErr := operation.Close()
		if closeErr == nil {
			selected.pending = selected.pending[:len(selected.pending)-1]
		}
		err = errors.Join(err, closeErr)
	}
	return errors.Join(err, selected.checkReadFilesV1(ctx))
}

func (selected *slotSnapshotReadV1) checkReadFilesV1(ctx context.Context) error {
	if len(selected.roles) != 17 || selected.diskInfo == nil || selected.metadataInfo == nil {
		return rewindFailureV1("the selected Snapshot lost its actual read custody")
	}
	for index, file := range []*os.File{selected.disk, selected.metadata} {
		want := selected.diskInfo
		if index == 1 {
			want = selected.metadataInfo
		}
		got, err := file.Stat()
		if err != nil {
			return err
		}
		if err := checkSlotSnapshotReadFileV1(file, got); err != nil {
			return err
		}
		if !os.SameFile(got, want) || got.Size() != want.Size() || !got.ModTime().Equal(want.ModTime()) {
			return rewindFailureV1("the selected Snapshot read cap changed identity, length or metadata")
		}
	}
	raw, err := io.ReadAll(io.NewSectionReader(selected.metadata, 0, (64<<10)+1))
	if err != nil {
		return err
	}
	metadata, err := checkpoint.DecodeMetadataV1(raw)
	if err != nil {
		return err
	}
	got, err := checkpoint.EncodeMetadataV1(metadata)
	if err != nil {
		return err
	}
	want, err := checkpoint.EncodeMetadataV1(selected.entry.Metadata)
	if err != nil {
		return err
	}
	if !bytes.Equal(got, want) {
		return rewindFailureV1("the actual selected metadata differs from the retained read cap")
	}
	return ctx.Err()
}

func (selected *slotSnapshotReadV1) Close() error {
	if selected == nil {
		return nil
	}
	selected.mu.Lock()
	if selected.issuer != selected {
		selected.mu.Unlock()
		return rewindFailureV1("a copied Snapshot cannot release the original read owner")
	}
	if selected.closed {
		err := selected.closeErr
		selected.mu.Unlock()
		return err
	}
	selected.closing = true
	selected.cancel()
	selected.mu.Unlock()
	selected.active.Wait()
	selected.operations.Lock()
	defer selected.operations.Unlock()
	selected.mu.Lock()
	defer selected.mu.Unlock()
	if selected.closed {
		return selected.closeErr
	}
	for _, operation := range selected.pending {
		selected.closeErr = errors.Join(selected.closeErr, operation.Close())
	}
	if selected.closeErr != nil {
		return selected.closeErr
	}
	for _, file := range append(append([]*os.File(nil), selected.roles...), selected.disk, selected.metadata) {
		if file != nil {
			selected.closeErr = errors.Join(selected.closeErr, file.Close())
		}
	}
	selected.closed = selected.closeErr == nil
	return selected.closeErr
}
