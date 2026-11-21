//go:build linux

package process

import (
	"context"
	"errors"
	"golang.org/x/sys/unix"
)

// CheckLiveV1 checks only the actual original factory/Start owner. It exposes
// no process number, root descriptor or reconstruction from guest metadata.
func (owner *CopyOwnerV1) CheckLiveV1(ctx context.Context) error {
	if owner == nil || owner.native == nil || owner.native.runtime == nil || ctx == nil {
		return setupError("the original process runtime owner is absent", nil)
	}
	native := owner.native
	runtime := native.runtime
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.copy != owner || native.closed.Load() || !native.ready.Load() || native.conn == nil || runtime.cmd == nil || runtime.closed || runtime.pidfd < 0 || runtime.link == nil {
		return setupError("the original process runtime ended or changed", nil)
	}
	polls := []unix.PollFd{{Fd: int32(runtime.pidfd), Events: unix.POLLIN}}
	count, err := unix.Poll(polls, 0)
	if err != nil || count != 0 {
		return setupError("the original process creation lifetime ended", err)
	}
	return errors.Join(ctx.Err())
}
