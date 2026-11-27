//go:build linux

package slotlaunch

import (
	"context"
	"errors"
	"io"
	"math"
	"os"

	"golang.org/x/sys/unix"
)

func (b *backend) stoppedCheckpointOperationV2(ctx context.Context, r Request) (Response, error) {
	if !nativeSocket(os.Stdout) {
		return Response{}, refused("the stopped checkpoint operation needs its original native channel")
	}
	record, lock, err := b.session(r.Session, false)
	if err != nil {
		return Response{}, err
	}
	defer lock.Close()
	store, err := b.lock("store/.lock", false)
	if err != nil {
		return Response{}, err
	}
	defer store.Close()
	initial, err := checkpointNativeReplyV1(Response{OK: true})
	if err != nil {
		return Response{}, err
	}
	err = sendNativeResponse(initial)
	initial.native.close()
	if err != nil {
		return Response{}, err
	}
	roles, err := receiveCheckpointReadFilesV1(18)
	if err != nil {
		return Response{}, err
	}
	defer func() {
		for _, file := range roles {
			file.Close()
		}
	}()
	check := func() error {
		return errors.Join(ctx.Err(), b.checkReservedSlotCoreV2(record, roles), requireOriginalSlotActorsExitedV2(roles))
	}
	if err := check(); err != nil {
		return Response{}, err
	}
	response := Response{OK: true}
	var outputs []*os.File
	switch r.Action {
	case "probe":
		if err := b.checkReservedSlotRolesV2(record, roles); err != nil {
			return Response{}, err
		}
		err = b.probeCheckpointV1(r.Session, roles[9], roles[16])
	case "space":
		if err := b.checkReservedSlotRolesV2(record, roles); err != nil {
			return Response{}, err
		}
		info, statErr := roles[9].Stat()
		if statErr != nil {
			return Response{}, statErr
		}
		err = checkSpaceAtCheckpointV2(roles[16], info.Size())
	case "list":
		response.Checkpoints, err = b.listCheckpointsV1(r.Session, roles[16])
	case "trim":
		err = b.trimCheckpointsV1(r.Session, roles[16], r.KeepCount)
	case "select":
		outputs, err = b.selectCheckpointV1(r.Session, r.Name, roles[16])
	case "create":
		if err := b.checkReservedSlotRolesV2(record, roles); err != nil {
			return Response{}, err
		}
		metadata := make([]byte, r.MetaSize)
		if _, err = io.ReadFull(os.Stdin, metadata); err == nil {
			err = b.createCheckpointV1(ctx, r.Session, r.Name, roles[9], roles[16], metadata, r.KeepCount, check)
		}
	default:
		err = refused("the stopped checkpoint action is not supported")
	}
	if err == nil {
		err = check()
	}
	if err != nil {
		for _, file := range outputs {
			file.Close()
		}
		return Response{}, err
	}
	return checkpointNativeReplyV1(response, outputs...)
}

func checkSpaceAtCheckpointV2(directory *os.File, needed int64) error {
	if directory == nil || needed < 1 {
		return refused("the original checkpoint space observation is incomplete")
	}
	var fs unix.Statfs_t
	if err := unix.Fstatfs(int(directory.Fd()), &fs); err != nil {
		return err
	}
	if fs.Bsize <= 0 {
		return refused("the original checkpoint filesystem has no usable block size")
	}
	free := int64(math.MaxInt64)
	if uint64(fs.Bavail) <= uint64(math.MaxInt64)/uint64(fs.Bsize) {
		free = int64(fs.Bavail) * int64(fs.Bsize)
	}
	if free < needed {
		return &launchError{Code: "checkpoint_low_disk", Message: "checkpoints paused: low disk", Hint: "Free space on the session's original checkpoint filesystem, then try again."}
	}
	return nil
}
