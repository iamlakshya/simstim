//go:build linux

package host

import (
	"context"
	"errors"
	"io"
	"os"
	"time"

	"github.com/iamlakshya/simstim/internal/checkpoint"
	"github.com/iamlakshya/simstim/internal/slotlaunch"
	"golang.org/x/sys/unix"
)

// SelectRunningSnapshotV1 obtains read caps before Stop. The actual root
// FICLONE and free-space checks run on this original disk first.
func (owner *SlotSnapshotOwnerV1) SelectRunningSnapshotV1(ctx context.Context, name string) (checkpoint.SnapshotV1, error) {
	done, err := owner.beginV1(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	owner.operations.Lock()
	defer owner.operations.Unlock()
	when, err := time.Parse(checkpoint.TimeLayout, name)
	if err != nil || when.Format(checkpoint.TimeLayout) != name {
		return nil, rewindFailureV1("the selected Snapshot has another fixed timestamp")
	}
	if err := owner.checkRunningV1(ctx); err != nil {
		return nil, err
	}
	for _, request := range []slotlaunch.CheckpointOperationV1{{Session: owner.operation.Primary(), Action: "probe"}, {Session: owner.operation.Primary(), Action: "space", Needed: owner.need.DiskMax}} {
		if _, err := owner.callRunningV1(ctx, request, nil); err != nil {
			return nil, err
		}
	}
	lifetime, cancel := context.WithCancel(context.WithoutCancel(ctx))
	selected := &slotSnapshotReadV1{origin: owner, lifetime: lifetime, cancel: cancel}
	selected.issuer = selected
	owner.incomplete = append(owner.incomplete, selected)
	_, err = owner.callRunningV1(ctx, slotlaunch.CheckpointOperationV1{Session: owner.operation.Primary(), Action: "select", Name: name}, func(files []*os.File) error {
		if len(files) != 2 {
			return rewindFailureV1("the actual root selection returned another read role vector")
		}
		for index, file := range files {
			duplicate, err := duplicateSlotSnapshotFileV1(file)
			if duplicate != nil {
				if index == 0 {
					selected.disk = duplicate
				} else {
					selected.metadata = duplicate
				}
			}
			if err != nil {
				return err
			}
		}
		return nil
	})
	if selected.disk == nil || selected.metadata == nil {
		return selected, errors.Join(err, rewindFailureV1("the actual root selection has incomplete read custody"))
	}
	// Retain native read operands only through this actual accepted source.
	// This private branch is not a descriptor-vector Source constructor.
	for _, file := range owner.roles {
		duplicate, dupErr := duplicateSlotSnapshotFileV1(file)
		if duplicate != nil {
			selected.roles = append(selected.roles, duplicate)
		}
		if dupErr != nil {
			return selected, errors.Join(err, dupErr)
		}
	}
	if readErr := selected.captureReadFilesV1(ctx, when, owner.need.DiskMax); readErr != nil {
		return selected, errors.Join(err, readErr)
	}
	if checkErr := owner.checkRunningV1(ctx); checkErr != nil {
		return selected, errors.Join(err, checkErr)
	}
	// A late transport/worker join error keeps this real read owner reachable.
	// Admission still requires all read checks; the error is never an ACK.
	selected.admitted = true
	for index, pending := range owner.incomplete {
		if pending == selected {
			owner.incomplete = append(owner.incomplete[:index], owner.incomplete[index+1:]...)
			break
		}
	}
	return selected, err
}

func (owner *SlotSnapshotOwnerV1) callRunningV1(ctx context.Context, request slotlaunch.CheckpointOperationV1, use func([]*os.File) error) (slotlaunch.Response, error) {
	if err := owner.checkRunningV1(ctx); err != nil {
		return slotlaunch.Response{}, err
	}
	borrowed, cancel := context.WithCancel(ctx)
	joined := make(chan struct{})
	stop := context.AfterFunc(owner.lifetime, func() { defer close(joined); cancel() })
	defer func() {
		cancel()
		if !stop() {
			<-joined
		}
	}()
	operation, err := slotlaunch.RunCheckpointOperationV1(borrowed, owner.roles, request)
	if operation == nil {
		return slotlaunch.Response{}, err
	}
	// Store before inspecting any result so failed ACK/Close cannot lose the
	// actual root worker or its partially materialised native output.
	owner.pending = append(owner.pending, operation)
	result := operation.ResultV1()
	if use != nil {
		err = errors.Join(err, operation.WithFilesV1(borrowed, use))
	}
	closeErr := operation.Close()
	if closeErr == nil {
		owner.pending = owner.pending[:len(owner.pending)-1]
	}
	return result, errors.Join(err, closeErr, owner.checkRunningV1(ctx))
}

func (selected *slotSnapshotReadV1) captureReadFilesV1(ctx context.Context, when time.Time, size int64) error {
	for index, file := range []*os.File{selected.disk, selected.metadata} {
		info, err := file.Stat()
		if err != nil {
			return err
		}
		if err := checkSlotSnapshotReadFileV1(file, info); err != nil {
			return err
		}
		if index == 0 {
			selected.diskInfo = info
		} else {
			selected.metadataInfo = info
		}
	}
	if selected.diskInfo.Size() != size || selected.metadataInfo.Size() <= 0 || selected.metadataInfo.Size() > 64<<10 {
		return rewindFailureV1("the actual selected disk or metadata has another admitted length")
	}
	raw, err := io.ReadAll(io.NewSectionReader(selected.metadata, 0, (64<<10)+1))
	if err != nil {
		return err
	}
	metadata, err := checkpoint.DecodeMetadataV1(raw)
	if err != nil {
		return err
	}
	selected.entry = checkpoint.Entry{Time: when, Metadata: metadata}
	return ctx.Err()
}

func checkSlotSnapshotReadFileV1(file *os.File, info os.FileInfo) error {
	if file == nil || info == nil {
		return rewindFailureV1("an original selected read cap ended")
	}
	var native unix.Stat_t
	flags, err := unix.FcntlInt(file.Fd(), unix.F_GETFL, 0)
	if err != nil || unix.Fstat(int(file.Fd()), &native) != nil || native.Uid != 0 || native.Nlink != 1 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || flags&unix.O_ACCMODE != unix.O_RDONLY || flags&unix.O_PATH != 0 {
		return errors.Join(rewindFailureV1("the selected Snapshot is not its root-issued read-only file"), err)
	}
	return nil
}
