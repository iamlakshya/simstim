package host

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// pidAlive reads this user's process state without wait. SZOMB is state 5
// in sys/proc.h, and means an exited child that its owner has not reaped.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	return err == nil && int(p.Proc.P_pid) == pid && int(p.Eproc.Ucred.Uid) == os.Geteuid() && p.Proc.P_stat != 0 && p.Proc.P_stat != 5
}

func holderProcessArgs(pid int) ([]string, error) {
	b, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return nil, fmt.Errorf("read holder process arguments: %w", err)
	}
	if len(b) < 4 {
		return nil, errors.New("holder process arguments have no count")
	}
	n := binary.NativeEndian.Uint32(b[:4])
	if n == 0 || n > 4096 {
		return nil, errors.New("holder process argument count is invalid")
	}
	// KERN_PROCARGS2 starts with argc, then an executable path, NUL padding,
	// argc NUL-terminated arguments and the environment. Read only arguments.
	_, b, ok := bytes.Cut(b[4:], []byte{0})
	if !ok {
		return nil, errors.New("holder process executable path is incomplete")
	}
	b = bytes.TrimLeft(b, "\x00")
	args := make([]string, 0, n)
	for range n {
		arg, rest, ok := bytes.Cut(b, []byte{0})
		if !ok {
			return nil, errors.New("holder process arguments are incomplete")
		}
		args = append(args, string(arg))
		b = rest
	}
	return args, nil
}
