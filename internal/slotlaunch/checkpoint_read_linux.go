//go:build linux

package slotlaunch

import (
	"context"
	"math"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// inspectCheckpointReadV1 checks already-issued immutable read caps. It never
// reads or replaces the current disk, assumes a stopped runtime, or issues an
// outside Source/stop grant. The genuine snapshot issuer remains separate.
func (b *backend) inspectCheckpointReadV1(ctx context.Context, r Request) (Response, error) {
	if !nativeSocket(os.Stdout) {
		return Response{}, refused("a checkpoint read check needs its native descriptor channel")
	}
	_, lock, err := b.session(r.Session, false)
	if err != nil {
		return Response{}, err
	}
	defer lock.Close()
	initial, err := checkpointNativeReplyV1(Response{OK: true})
	if err != nil {
		return Response{}, err
	}
	err = sendNativeResponse(initial)
	initial.native.close()
	if err != nil {
		return Response{}, err
	}
	inputs, err := receiveCheckpointReadFilesV1(3)
	if err != nil {
		return Response{}, err
	}
	defer func() {
		for _, file := range inputs {
			file.Close()
		}
	}()
	if err := b.checkCheckpointDirectoryV1(r.Session, inputs[0]); err != nil {
		return Response{}, err
	}
	if r.Op == "checkpoint.discard-v2" {
		for index, suffix := range []string{".json", ".disk"} {
			file := inputs[2-index]
			held, err := file.Stat()
			if err != nil {
				return Response{}, err
			}
			flags, err := unix.FcntlInt(file.Fd(), unix.F_GETFL, 0)
			if err != nil || !held.Mode().IsRegular() || !owned(held, 0) || held.Mode().Perm() != 0o600 || flags&unix.O_ACCMODE != unix.O_RDONLY {
				return Response{}, refused("the original checkpoint creator cleanup has another native file role")
			}
			name := "vms/" + r.Session + "/checkpoints/" + r.Name + suffix
			current, err := b.root.Lstat(name)
			if os.IsNotExist(err) && held.Sys().(*syscall.Stat_t).Nlink == 0 {
				continue
			}
			if err != nil || !os.SameFile(held, current) {
				return Response{}, refused("the actual created checkpoint pair changed before discard")
			}
			if err := b.removeCapturedFile(name, file); err != nil {
				return Response{}, err
			}
		}
		if err := inputs[0].Sync(); err != nil {
			return Response{}, err
		}
		return checkpointNativeReplyV1(Response{OK: true})
	}
	files, err := b.selectCheckpointV1(r.Session, r.Name, inputs[0])
	if err != nil {
		return Response{}, err
	}
	defer func() {
		for _, file := range files {
			file.Close()
		}
	}()
	for index, file := range files {
		want, err := file.Stat()
		if err != nil {
			return Response{}, err
		}
		got, err := inputs[index+1].Stat()
		flags, flagErr := unix.FcntlInt(inputs[index+1].Fd(), unix.F_GETFL, 0)
		if err != nil || flagErr != nil || !singleRegular(got, 0) || got.Mode().Perm() != 0o600 || flags&unix.O_ACCMODE != unix.O_RDONLY || !os.SameFile(want, got) || want.Size() != got.Size() || !want.ModTime().Equal(got.ModTime()) {
			return Response{}, refused("the selected checkpoint read cap changed its original named root file")
		}
	}
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	if err := b.checkCheckpointDirectoryV1(r.Session, inputs[0]); err != nil {
		return Response{}, err
	}
	return checkpointNativeReplyV1(Response{OK: true})
}

func receiveCheckpointReadFilesV1(count int) (_ []*os.File, returned error) {
	control := make([]byte, unix.CmsgSpace(count*4))
	var marker [1]byte
	n, cn, flags, _, err := unix.Recvmsg(0, marker[:], control, unix.MSG_CMSG_CLOEXEC)
	if err != nil || n != 1 || marker[0] != 1 || flags&(unix.MSG_CTRUNC|unix.MSG_TRUNC) != 0 {
		closeReceivedRights(control[:cn])
		return nil, refused("the checkpoint read caps were truncated")
	}
	messages, err := unix.ParseSocketControlMessage(control[:cn])
	if err != nil {
		closeReceivedRights(control[:cn])
		return nil, err
	}
	var files []*os.File
	complete := false
	defer func() {
		if !complete {
			for _, file := range files {
				file.Close()
			}
		}
	}()
	unknown := false
	for _, message := range messages {
		if message.Header.Level != unix.SOL_SOCKET || message.Header.Type != unix.SCM_RIGHTS {
			unknown = true
			continue
		}
		fds, err := unix.ParseUnixRights(&message)
		if err != nil {
			return nil, err
		}
		for _, fd := range fds {
			files = append(files, os.NewFile(uintptr(fd), "original-checkpoint-read-cap"))
		}
	}
	if unknown || len(files) != count {
		return nil, refused("the checkpoint read check has another native role vector")
	}
	complete = true
	return files, nil
}

// Space is an observation of the original checkpoint target filesystem. It
// is neither a capacity grant nor a reason to hide an existing checkpoint.
func (b *backend) inspectCheckpointSpaceV1(ctx context.Context, r Request) (Response, error) {
	if !nativeSocket(os.Stdout) {
		return Response{}, refused("a checkpoint space check needs its native descriptor channel")
	}
	record, lock, err := b.session(r.Session, false)
	if err != nil {
		return Response{}, err
	}
	defer lock.Close()
	initial, err := checkpointNativeReplyV1(Response{OK: true})
	if err != nil {
		return Response{}, err
	}
	err = sendNativeResponse(initial)
	initial.native.close()
	if err != nil {
		return Response{}, err
	}
	inputs, err := receiveCheckpointReadFilesV1(2)
	if err != nil {
		return Response{}, err
	}
	defer func() {
		for _, file := range inputs {
			file.Close()
		}
	}()
	if err := b.checkCheckpointDirectoryV1(r.Session, inputs[0]); err != nil {
		return Response{}, err
	}
	current, err := b.open("vms/"+r.Session+"/disk.img", unix.O_RDONLY, 0)
	if err != nil {
		return Response{}, err
	}
	defer current.Close()
	want, err := current.Stat()
	if err != nil {
		return Response{}, err
	}
	got, err := inputs[1].Stat()
	if err != nil || !os.SameFile(want, got) || !want.Mode().IsRegular() || !singleRegular(want, record.SlotUID) && !singleRegular(want, b.caller.uid) || want.Size() != r.Needed {
		return Response{}, refused("the space check size is not the original source disk's fixed admitted length")
	}
	var space unix.Statfs_t
	if err := unix.Fstatfs(int(inputs[0].Fd()), &space); err != nil {
		return Response{}, err
	}
	if space.Bsize <= 0 {
		return Response{}, refused("the original checkpoint filesystem has no usable block size")
	}
	free := int64(math.MaxInt64)
	if uint64(space.Bavail) <= uint64(math.MaxInt64)/uint64(space.Bsize) {
		free = int64(space.Bavail) * int64(space.Bsize)
	}
	if free < r.Needed {
		return Response{}, &launchError{Code: "checkpoint_low_disk", Message: "checkpoints paused: low disk", Hint: "Free space on the session's original checkpoint filesystem, then try again."}
	}
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	if err := b.checkCheckpointDirectoryV1(r.Session, inputs[0]); err != nil {
		return Response{}, err
	}
	return checkpointNativeReplyV1(Response{OK: true})
}
