//go:build linux

package guest

import (
	"os"

	"golang.org/x/sys/unix"
)

// captureSessionFreezeV1 is called by the actual mount creator, before any
// pivot or agent. The retained descriptor names vdd's mounted filesystem.
func (v *vmFiles) captureSessionFreezeV1() error {
	if v.sessionMount != nil || v.sessionFreeze != nil {
		return freezeErrorV1("the original session mount was already captured", nil)
	}
	fd, err := unix.Open(v.boot("mnt/session"), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return freezeErrorV1("cannot retain the original session mount", err)
	}
	file := os.NewFile(uintptr(fd), "session-mount")
	v.sessionMount = file
	v.sessionFreeze = &filesystemFreezeV1{change: func(freeze bool) error {
		request := uint(0xc0045878) // FIFREEZE, _IOWR('X',120,int)
		if !freeze {
			request = 0xc0045879
		} // FITHAW
		return unix.IoctlSetInt(int(file.Fd()), request, 0)
	}}
	return nil
}

func (v *vmFiles) closeSessionFreezeV1() error {
	if v == nil {
		return nil
	}
	if err := v.sessionFreeze.close(); err != nil {
		return err
	}
	if v.sessionMount != nil {
		err := v.sessionMount.Close()
		v.sessionMount = nil
		return err
	}
	return nil
}

func (v *vmRuntime) sessionFreezeV1() (*filesystemFreezeV1, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.ctx == nil || v.ctx.Err() != nil || !v.applied || v.files == nil || v.files.sessionFreeze == nil {
		return nil, freezeErrorV1("the original microvm session mount is not live", nil)
	}
	return v.files.sessionFreeze, nil
}
