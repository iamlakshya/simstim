//go:build linux

package slotlaunch

import (
	"context"
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// The root input is actual native DATA/reservation received on this SAME
// creator transaction. No host path, Spec field or presence bool supplies it.
func (b *backend) startWithNativeInputsV2(ctx context.Context, r Request) (Response, error) {
	if !nativeSocket(os.Stdout) {
		return Response{}, refused("the new root input requires its original native creator channel")
	}
	initial, err := checkpointNativeReplyV1(Response{OK: true})
	if err != nil {
		return Response{}, err
	}
	err = sendNativeResponse(initial)
	initial.native.close()
	if err != nil {
		return Response{}, err
	}
	count := 2
	if r.Op == "start.reserved-v2" {
		count = 19
	}
	files, err := receiveCheckpointReadFilesV1(count)
	if err != nil {
		return Response{}, err
	}
	defer func() {
		for _, file := range files {
			file.Close()
		}
	}()
	input := &rootStartInputsV2{}
	if r.Op == "start.reserved-v2" {
		input.reserved = files
	} else {
		input.fork = files[0]
		if err := checkCurrentStartParentV2(files[1]); err != nil {
			return Response{}, err
		}
	}
	r.Op = "start" // The canonical execution request remains the original shape.
	return b.startWithInputsV2(ctx, r, input)
}

func checkCurrentStartParentV2(parent *os.File) error {
	if parent == nil {
		return refused("the actual new supervisor parent cgroup is absent")
	}
	current, err := captureCurrentCgroup()
	if err != nil {
		return err
	}
	defer current.Close()
	want, err := current.Stat()
	if err != nil {
		return err
	}
	got, err := parent.Stat()
	var fs unix.Statfs_t
	if err != nil || !os.SameFile(want, got) || unix.Fstatfs(int(parent.Fd()), &fs) != nil || fs.Type != unix.CGROUP2_SUPER_MAGIC {
		return refused("sudo moved the new root creator outside the actual new supervisor cgroup")
	}
	return nil
}

func (b *backend) copyOriginalForkInputV2(ctx context.Context, r Request, input, target *os.File, slotUID int) error {
	want, err := input.Stat()
	if err != nil || !singleRegular(want, b.caller.uid) || want.Mode().Perm() != 0o600 || want.Size() != r.DiskMaxGiB*(1<<30) {
		return refused("the fork input is not its original private admitted raw disk")
	}
	flags, err := unix.FcntlInt(input.Fd(), unix.F_GETFL, 0)
	if err != nil || flags&unix.O_ACCMODE == unix.O_WRONLY || flags&unix.O_PATH != 0 {
		return refused("the actual fork input has no readable native data role")
	}
	output, err := target.Stat()
	if err != nil || !singleRegular(output, slotUID) || output.Mode().Perm() != 0o660 || output.Size() != want.Size() || os.SameFile(output, want) {
		return refused("the actual fresh root fork target has another native role")
	}
	err = unix.IoctlFileClone(int(target.Fd()), int(input.Fd()))
	if err != nil {
		if !errors.Is(err, unix.EOPNOTSUPP) && !errors.Is(err, unix.ENOTTY) && !errors.Is(err, unix.EXDEV) && !errors.Is(err, unix.EINVAL) {
			return err
		}
		if err := target.Truncate(0); err != nil {
			return err
		}
		if err := copySparseForkDiskV1(ctx, input, target, want.Size()); err != nil {
			return err
		}
	}
	got, err := input.Stat()
	if err != nil || !os.SameFile(got, want) || got.Size() != want.Size() || !got.ModTime().Equal(want.ModTime()) || got.Sys().(*syscall.Stat_t).Nlink != 1 {
		return refused("the original fork input changed during its actual root copy")
	}
	return errors.Join(ctx.Err(), target.Sync())
}
