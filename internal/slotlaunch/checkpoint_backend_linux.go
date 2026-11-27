//go:build linux

package slotlaunch

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/checkpoint"
	"golang.org/x/sys/unix"
)

type slotCheckpointBackendV1 struct {
	custody *CheckpointCustodyV1
	created map[string]*slotCreatedCheckpointV2
}

type slotCreatedCheckpointV2 struct {
	disk, metadata *os.File
	entry          checkpoint.Entry
}

// CheckpointBackendV1 projects only this retained original slot custody. The
// backend is borrowed until custody.Close; selected snapshots own independent
// copies and may survive that close and the original VMM's physical stop.
func (custody *CheckpointCustodyV1) CheckpointBackendV1(ctx context.Context) (checkpoint.BackendV1, error) {
	done, err := custody.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	if err := custody.checkRunning(ctx); err != nil {
		return nil, err
	}
	return &slotCheckpointBackendV1{custody: custody}, nil
}

func (backend *slotCheckpointBackendV1) Probe(ctx context.Context) error {
	_, files, err := backend.custody.callRunning(ctx, "probe", "", 0, nil)
	for _, file := range files {
		file.Close()
	}
	return err
}

func (backend *slotCheckpointBackendV1) CheckSpaceV1(ctx context.Context, needed int64) error {
	custody := backend.custody
	if needed < 1 || custody == nil || custody.source == nil || needed != custody.source.request.DiskMaxGiB*(1<<30) {
		return refused("the checkpoint space size differs from its original admitted disk")
	}
	// Opening the original protected target is independent of the free-space
	// result, so listing or selecting checkpoints remains available.
	if _, files, err := custody.callRunning(ctx, "list", "", 0, nil); err != nil {
		return err
	} else {
		for _, file := range files {
			file.Close()
		}
	}
	done, err := custody.begin(ctx)
	if err != nil {
		return err
	}
	defer done()
	custody.operations.Lock()
	defer custody.operations.Unlock()
	if err := custody.checkRunning(ctx); err != nil {
		return err
	}
	info, err := custody.source.files[9].Stat()
	if err != nil || info.Size() != needed {
		return refused("the original checkpoint disk changed its fixed length")
	}
	roles := append([]*os.File(nil), custody.source.files...)
	roles = append(roles, custody.source.parent, custody.vmPeer, custody.guest, custody.checkpoints)
	operation, err := RunCheckpointOperationV1(ctx, roles, CheckpointOperationV1{Session: custody.source.request.Session, Action: "space", Needed: needed})
	if operation != nil {
		err = errors.Join(err, operation.Close())
	}
	return errors.Join(err, custody.checkRunning(ctx))
}

func (backend *slotCheckpointBackendV1) Create(ctx context.Context, at time.Time, metadata checkpoint.Metadata, keep int) (checkpoint.Entry, error) {
	data, err := checkpoint.EncodeMetadataV1(metadata)
	if err != nil {
		return checkpoint.Entry{}, err
	}
	name := at.UTC().Format(checkpoint.TimeLayout)
	if at.IsZero() {
		return checkpoint.Entry{}, refused("a checkpoint needs its actual creation time")
	}
	when, err := time.Parse(checkpoint.TimeLayout, name)
	if err != nil {
		return checkpoint.Entry{}, err
	}
	copy, err := checkpoint.DecodeMetadataV1(data)
	entry := checkpoint.Entry{Time: when, Metadata: copy}
	custody := backend.custody
	if _, files, openErr := custody.callRunning(ctx, "list", "", 0, nil); openErr != nil {
		return checkpoint.Entry{}, openErr
	} else {
		for _, file := range files {
			file.Close()
		}
	}
	done, err := custody.begin(ctx)
	if err != nil {
		return checkpoint.Entry{}, err
	}
	defer done()
	custody.operations.Lock()
	defer custody.operations.Unlock()
	if err := custody.checkRunning(ctx); err != nil {
		return checkpoint.Entry{}, err
	}
	_, files, createErr := custody.runningExchange(ctx, "create", name, keep, data)
	if len(files) == 2 {
		if backend.created == nil {
			backend.created = make(map[string]*slotCreatedCheckpointV2)
		}
		if old := backend.created[name]; old != nil {
			for _, file := range files {
				file.Close()
			}
			return entry, errors.Join(createErr, refused("the actual checkpoint creation token was repeated"))
		}
		backend.created[name] = &slotCreatedCheckpointV2{disk: files[0], metadata: files[1], entry: entry}
	} else {
		for _, file := range files {
			file.Close()
		}
		if createErr == nil {
			createErr = refused("the actual checkpoint creator returned no native file pair")
		}
	}
	return entry, errors.Join(createErr, err)
}

// DiscardCreatedV1 uses only this backend's actual retained creator pair. An
// Entry/time is a lookup key and cannot mint deletion of an older snapshot.
func (backend *slotCheckpointBackendV1) DiscardCreatedV1(ctx context.Context, entry checkpoint.Entry) error {
	key := entry.Time.UTC().Format(checkpoint.TimeLayout)
	created := backend.created[key]
	if created == nil {
		return refused("this backend has no actual creator for the checkpoint pair")
	}
	c := backend.custody
	if ctx == nil {
		return refused("the original creator cleanup has no lifetime")
	}
	cleanup := context.WithoutCancel(ctx)
	c.mu.Lock()
	if c.closing || c.original == nil || c.source == nil {
		c.mu.Unlock()
		return refused("the actual checkpoint creator is closing")
	}
	c.active.Add(1)
	c.mu.Unlock()
	defer c.active.Done()
	c.operations.Lock()
	defer c.operations.Unlock()
	roles := append([]*os.File(nil), c.source.files...)
	roles = append(roles, c.source.parent, c.vmPeer, c.guest, c.checkpoints, created.disk, created.metadata)
	operation, err := RunCheckpointOperationV1(cleanup, roles, CheckpointOperationV1{Session: c.source.request.Session, Action: "discard", Name: key})
	if operation != nil {
		err = errors.Join(err, operation.Close())
	}
	if err == nil {
		err = errors.Join(created.disk.Close(), created.metadata.Close())
		delete(backend.created, key)
	}
	return err
}

// CloseCreatedV1 releases this operation's local creator caps after the native
// worker and any explicit discard joined. It deletes no completed checkpoint.
func (backend *slotCheckpointBackendV1) CloseCreatedV1() error {
	var result error
	for key, created := range backend.created {
		result = errors.Join(result, created.disk.Close(), created.metadata.Close())
		delete(backend.created, key)
	}
	return result
}

func (backend *slotCheckpointBackendV1) Trim(ctx context.Context, keep int) error {
	_, files, err := backend.custody.callRunning(ctx, "trim", "", keep, nil)
	for _, file := range files {
		file.Close()
	}
	return err
}

func (backend *slotCheckpointBackendV1) List(ctx context.Context) ([]checkpoint.Entry, error) {
	response, files, err := backend.custody.callRunning(ctx, "list", "", 0, nil)
	for _, file := range files {
		file.Close()
	}
	if err != nil {
		return nil, err
	}
	if len(response.Checkpoints) > 1024 {
		return nil, refused("the checkpoint list exceeds its bound")
	}
	result := make([]checkpoint.Entry, 0, len(response.Checkpoints))
	for _, name := range response.Checkpoints {
		selected, err := backend.Select(ctx, name)
		if err != nil {
			return nil, err
		}
		result = append(result, selected.Entry())
		if err := selected.Close(); err != nil {
			return nil, err
		}
	}
	return result, nil
}

type slotCheckpointSnapshotV1 struct {
	mu                     sync.Mutex
	custody                *CheckpointCustodyV1
	disk, metadata         *os.File
	entry                  checkpoint.Entry
	diskInfo, metadataInfo os.FileInfo
	closed                 bool
}

func (backend *slotCheckpointBackendV1) Select(ctx context.Context, name string) (checkpoint.SnapshotV1, error) {
	_, files, err := backend.custody.callRunning(ctx, "select", name, 0, nil)
	if err != nil {
		return nil, err
	}
	if len(files) != 2 {
		for _, file := range files {
			file.Close()
		}
		return nil, refused("the selected checkpoint lacks its original disk and metadata roles")
	}
	complete := false
	defer func() {
		if !complete {
			for _, file := range files {
				file.Close()
			}
		}
	}()
	for _, file := range files {
		info, err := file.Stat()
		flags, flagErr := unix.FcntlInt(file.Fd(), unix.F_GETFL, 0)
		if err != nil || flagErr != nil || !singleRegular(info, 0) || info.Mode().Perm() != 0o600 || flags&unix.O_ACCMODE != unix.O_RDONLY {
			return nil, refused("the selected checkpoint is not its root-issued read-only native file")
		}
	}
	info, err := files[0].Stat()
	if err != nil || info.Size() != backend.custody.source.request.DiskMaxGiB*(1<<30) {
		return nil, refused("the selected checkpoint disk has another fixed size")
	}
	metadataInfo, err := files[1].Stat()
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(files[1], (64<<10)+1))
	if err != nil {
		return nil, err
	}
	metadata, err := checkpoint.DecodeMetadataV1(data)
	if err != nil {
		return nil, err
	}
	when, err := time.Parse(checkpoint.TimeLayout, name)
	if err != nil || when.Format(checkpoint.TimeLayout) != name {
		return nil, refused("the root checkpoint selected another timestamp role")
	}
	retained, err := backend.custody.clone(ctx)
	if err != nil {
		return nil, err
	}
	complete = true
	return &slotCheckpointSnapshotV1{custody: retained, disk: files[0], metadata: files[1], entry: checkpoint.Entry{Time: when, Metadata: metadata}, diskInfo: info, metadataInfo: metadataInfo}, nil
}

func (selected *slotCheckpointSnapshotV1) DiskBytesV1() int64 {
	if selected == nil {
		return 0
	}
	selected.mu.Lock()
	defer selected.mu.Unlock()
	if selected.diskInfo == nil {
		return 0
	}
	return selected.diskInfo.Size()
}

func (selected *slotCheckpointSnapshotV1) CheckV1(ctx context.Context) error {
	if selected == nil || ctx == nil {
		return refused("the selected checkpoint has no original read-cap owner")
	}
	selected.mu.Lock()
	defer selected.mu.Unlock()
	if selected.closed || selected.custody == nil || selected.diskInfo == nil || selected.metadataInfo == nil {
		return refused("the original selected checkpoint read caps ended")
	}
	if err := selected.checkReadFilesV1(ctx); err != nil {
		return err
	}
	done, err := selected.custody.begin(ctx)
	if err != nil {
		return err
	}
	defer done()
	selected.custody.operations.Lock()
	defer selected.custody.operations.Unlock()
	custody := selected.custody
	if custody.original == nil || custody.source == nil || !custody.source.adopted || len(custody.source.files) != 13 || custody.checkpoints == nil {
		return refused("the selected read caps have no original private issuer")
	}
	roles := append([]*os.File(nil), custody.source.files...)
	roles = append(roles, custody.source.parent, custody.vmPeer, custody.guest, custody.checkpoints, selected.disk, selected.metadata)
	operation, err := RunCheckpointOperationV1(ctx, roles, CheckpointOperationV1{Session: custody.source.request.Session, Action: "check", Name: selected.entry.Time.Format(checkpoint.TimeLayout)})
	if operation != nil {
		err = errors.Join(err, operation.Close())
	}
	if err != nil {
		return err
	}
	return selected.checkReadFilesV1(ctx)
}

func (selected *slotCheckpointSnapshotV1) checkReadFilesV1(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for index, file := range []*os.File{selected.disk, selected.metadata} {
		want := selected.diskInfo
		if index == 1 {
			want = selected.metadataInfo
		}
		got, err := file.Stat()
		flags, flagErr := unix.FcntlInt(file.Fd(), unix.F_GETFL, 0)
		if err != nil || flagErr != nil || !singleRegular(got, 0) || got.Mode().Perm() != 0o600 || flags&unix.O_ACCMODE != unix.O_RDONLY || !os.SameFile(want, got) || got.Size() != want.Size() || !got.ModTime().Equal(want.ModTime()) {
			return refused("an original selected checkpoint read cap changed identity or metadata")
		}
	}
	actual, err := io.ReadAll(io.NewSectionReader(selected.metadata, 0, (64<<10)+1))
	if err != nil {
		return err
	}
	metadata, err := checkpoint.DecodeMetadataV1(actual)
	if err != nil {
		return err
	}
	want, err := checkpoint.EncodeMetadataV1(selected.entry.Metadata)
	if err != nil {
		return err
	}
	got, err := checkpoint.EncodeMetadataV1(metadata)
	if err != nil {
		return err
	}
	if !bytes.Equal(want, got) {
		return refused("the selected checkpoint metadata no longer matches its actual read cap")
	}
	return nil
}

func (selected *slotCheckpointSnapshotV1) Entry() checkpoint.Entry {
	if selected == nil {
		return checkpoint.Entry{}
	}
	selected.mu.Lock()
	defer selected.mu.Unlock()
	data, err := checkpoint.EncodeMetadataV1(selected.entry.Metadata)
	if err != nil {
		return checkpoint.Entry{}
	}
	copy, err := checkpoint.DecodeMetadataV1(data)
	if err != nil {
		return checkpoint.Entry{}
	}
	return checkpoint.Entry{Time: selected.entry.Time, Metadata: copy}
}

func (selected *slotCheckpointSnapshotV1) Close() error {
	if selected == nil {
		return nil
	}
	selected.mu.Lock()
	defer selected.mu.Unlock()
	if selected.closed {
		return nil
	}
	selected.closed = true
	return errors.Join(selected.disk.Close(), selected.metadata.Close(), selected.custody.Close())
}

func (backend *slotCheckpointBackendV1) PrepareRestore(ctx context.Context, snapshot checkpoint.SnapshotV1, transaction checkpoint.TransactionV1, checkStopped func(context.Context) error) (checkpoint.PreparedRestoreV1, error) {
	if err := checkpoint.ValidatePrepareV1(transaction); err != nil {
		return nil, err
	}
	selected, ok := snapshot.(*slotCheckpointSnapshotV1)
	if !ok || selected == nil || checkStopped == nil || selected.custody == nil || selected.custody.original != backend.custody.original {
		return nil, refused("rewind needs the same actual root-issued checkpoint and original stopped slot custody")
	}
	// The stopped receiver must come from the outside SAMEOP lifecycle owner.
	// The running source is deliberately unable to convert itself using a
	// callback, journal, elapsed time or the result of a stop request.
	return nil, refused("the original slot stopped-storage receiver has not been retained")
}
