//go:build linux

package supply

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// This is correlation DATA from the SAME original C, not an execution grant.
// Only its original private native owner can accept the captured nonce/pipe.
func WriteVMMReadonlyProjectionV1(ctx context.Context, writer, program, lease *os.File, item Item, nonce [16]byte) error {
	intent, err := readExecModeIntent(lease)
	if err != nil {
		return err
	}
	_, p, l, err := checkVMMExecModeChildShape(ctx, item, program, lease, 0o555, intent.Armed, false)
	if err != nil {
		return err
	}
	var fs unix.Statfs_t
	if err := unix.Fstatfs(int(program.Fd()), &fs); err != nil {
		return err
	}
	if fs.Flags&unix.ST_RDONLY == 0 {
		return fmt.Errorf("original read-only VMM projection lost its read-only mount")
	}
	if writer == nil {
		return fmt.Errorf("original read-only VMM projection lost its sentinel")
	}
	var pipe unix.Stat_t
	if err := errors.Join(unix.Fstat(int(writer.Fd()), &pipe), unix.Fstatfs(int(writer.Fd()), &fs)); err != nil {
		return err
	}
	flags, err := unix.FcntlInt(writer.Fd(), unix.F_GETFL, 0)
	if err != nil {
		return err
	}
	if pipe.Mode&unix.S_IFMT != unix.S_IFIFO || fs.Type != 0x50495045 || flags&unix.O_ACCMODE != unix.O_WRONLY {
		return fmt.Errorf("original read-only projection has another sentinel role")
	}
	dumpable, err := unix.PrctlRetInt(unix.PR_GET_DUMPABLE, 0, 0, 0, 0)
	if err != nil || dumpable != 0 {
		return errors.Join(err, fmt.Errorf("original read-only projection is not non-dumpable"))
	}
	record := ExecModeRecordV1{Phase: ExecModeProjectionV1, ProgramDev: uint64(p.Dev), ProgramIno: uint64(p.Ino), LeaseDev: uint64(l.Dev), LeaseIno: uint64(l.Ino), Nonce: nonce, PID: uint64(os.Getpid())}
	digest, _ := hex.DecodeString(item.FSVerity)
	copy(record.FSVerity[:], digest)
	return writeExecModeRecordV1(writer, record)
}
