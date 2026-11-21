//go:build linux

package supply

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// These functions verify inherited DATA capabilities. Only the original
// private Cmd stage may retain them or accept their records. Neither a raw
// fd vector, record nor PID can mint a mode phase or execution authority.
func CheckProjectedVMMExecModeV1(ctx context.Context, item Item, program, lease *os.File) error {
	_, _, _, err := checkVMMExecModeChild(ctx, item, program, lease, 0o555, false)
	return err
}
func CheckArmedVMMExecModeV1(ctx context.Context, item Item, program, lease *os.File) error {
	_, _, _, err := checkVMMExecModeChild(ctx, item, program, lease, 0o111, true)
	return err
}

func checkVMMExecModeChild(ctx context.Context, item Item, program, lease *os.File, mode os.FileMode, armed bool) (execModeIntent, unix.Stat_t, unix.Stat_t, error) {
	var value execModeIntent
	var p, l unix.Stat_t
	if err := ctx.Err(); err != nil {
		return value, p, l, err
	}
	if item.Kind != KindVMM || program == nil || lease == nil {
		return value, p, l, fmt.Errorf("VMM mode stage lost its private native roles")
	}
	if err := validateSealedRuntimeItem(item); err != nil {
		return value, p, l, err
	}
	var err error
	value, err = readExecModeIntent(lease)
	if err != nil {
		return value, p, l, err
	}
	if value.Version != 1 || value.Role != execModeRole || value.Armed != armed || value.SHA256 != item.SHA256 || value.FSVerity != item.FSVerity || value.Arch != item.Arch || value.Size != item.Size || !value.Self.Known() || !value.Artifact.Known() {
		return value, p, l, fmt.Errorf("VMM mode stage has another original native intent")
	}
	if err := unix.Fstat(int(program.Fd()), &p); err != nil {
		return value, p, l, err
	}
	if err := unix.Fstat(int(lease.Fd()), &l); err != nil {
		return value, p, l, err
	}
	pi, err := program.Stat()
	if err != nil {
		return value, p, l, err
	}
	li, err := lease.Stat()
	if err != nil {
		return value, p, l, err
	}
	if !pi.Mode().IsRegular() || pi.Mode() != mode || pi.Size() != item.Size || p.Nlink > 1 || !li.Mode().IsRegular() || li.Mode() != 0o600 || l.Nlink != 1 {
		return value, p, l, fmt.Errorf("VMM mode stage changed native inode, mode or size")
	}
	if !sameMappedModeIdentity(value.Artifact.Identity, p) || !sameMappedModeIdentity(value.Self.Identity, l) {
		return value, p, l, fmt.Errorf("VMM mode stage substituted an original inode")
	}
	for _, file := range []*os.File{program, lease} {
		flags, err := unix.FcntlInt(file.Fd(), unix.F_GETFL, 0)
		if err != nil {
			return value, p, l, err
		}
		if flags&unix.O_ACCMODE != unix.O_RDONLY {
			return value, p, l, fmt.Errorf("VMM mode stage received writable native rights")
		}
	}
	got, err := measureFSVerity(program)
	if err != nil {
		return value, p, l, err
	}
	if err := compareVerity(item, got); err != nil {
		return value, p, l, err
	}
	if _, err := hex.DecodeString(value.Nonce); err != nil || len(value.Nonce) != 32 {
		return value, p, l, fmt.Errorf("VMM mode stage has an invalid original phase nonce")
	}
	return value, p, l, ctx.Err()
}

func sameMappedModeIdentity(identity string, stat unix.Stat_t) bool {
	parts := strings.Split(identity, ":")
	if len(parts) != 4 || parts[0] != "unix" {
		return false
	}
	dev, e1 := strconv.ParseUint(parts[1], 10, 64)
	ino, e2 := strconv.ParseUint(parts[2], 10, 64)
	_, e3 := strconv.ParseUint(parts[3], 10, 32)
	// The original ownership was checked BEFORE namespace mapping. This
	// comparison cannot pretend mapped UID0 equals a subordinate helper UID1.
	return e1 == nil && e2 == nil && e3 == nil && dev == uint64(stat.Dev) && ino == uint64(stat.Ino)
}

func WriteVMMExecProjectionV1(ctx context.Context, writer, program, lease *os.File, item Item) error {
	return writeVMMExecStage(ctx, writer, program, lease, item, ExecModeProjectionV1, 0o555, false)
}
func WriteVMMExecArmedV1(ctx context.Context, writer, program, lease *os.File, item Item) error {
	return writeVMMExecStage(ctx, writer, program, lease, item, ExecModeArmedV1, 0o111, true)
}
func writeVMMExecStage(ctx context.Context, writer, program, lease *os.File, item Item, phase byte, mode os.FileMode, armed bool) error {
	value, p, l, err := checkVMMExecModeChild(ctx, item, program, lease, mode, armed)
	if err != nil {
		return err
	}
	if writer == nil {
		return fmt.Errorf("VMM mode stage lost its original sentinel writer")
	}
	var pipe unix.Stat_t
	var filesystem unix.Statfs_t
	if err := unix.Fstat(int(writer.Fd()), &pipe); err != nil {
		return err
	}
	if err := unix.Fstatfs(int(writer.Fd()), &filesystem); err != nil {
		return err
	}
	flags, err := unix.FcntlInt(writer.Fd(), unix.F_GETFL, 0)
	if err != nil {
		return err
	}
	if pipe.Mode&unix.S_IFMT != unix.S_IFIFO || filesystem.Type != 0x50495045 || flags&unix.O_ACCMODE != unix.O_WRONLY {
		return fmt.Errorf("VMM mode sentinel is not the original anonymous pipe writer")
	}
	dumpable, _, errno := unix.Syscall6(unix.SYS_PRCTL, unix.PR_GET_DUMPABLE, 0, 0, 0, 0, 0)
	if errno != 0 || dumpable != 0 {
		return fmt.Errorf("VMM mode stage is not actually non-dumpable")
	}
	record := ExecModeRecordV1{Phase: phase, ProgramDev: uint64(p.Dev), ProgramIno: uint64(p.Ino), LeaseDev: uint64(l.Dev), LeaseIno: uint64(l.Ino), PID: uint64(os.Getpid())}
	nonce, _ := hex.DecodeString(value.Nonce)
	digest, _ := hex.DecodeString(value.FSVerity)
	copy(record.Nonce[:], nonce)
	copy(record.FSVerity[:], digest)
	return writeExecModeRecordV1(writer, record)
}

func (lease *ExecModeLeaseV1) CheckProjectionRecordV1(ctx context.Context, record ExecModeRecordV1) error {
	return lease.checkRecord(ctx, record, ExecModeProjectionV1)
}
func (lease *ExecModeLeaseV1) CheckArmedRecordV1(ctx context.Context, record ExecModeRecordV1) error {
	return lease.checkRecord(ctx, record, ExecModeArmedV1)
}
func (lease *ExecModeLeaseV1) checkRecord(ctx context.Context, record ExecModeRecordV1, phase byte) error {
	if lease == nil || lease.state == nil {
		return fmt.Errorf("VMM mode source is missing")
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if record.Phase != phase {
		return fmt.Errorf("VMM mode record arrived in another native phase")
	}
	if phase == ExecModeProjectionV1 && state.armed || phase == ExecModeArmedV1 && !state.armed {
		return fmt.Errorf("VMM mode source has another actual state")
	}
	if err := state.checkProgram(ctx, state.program); err != nil {
		return err
	}
	var p, l unix.Stat_t
	if err := unix.Fstat(int(state.program.Fd()), &p); err != nil {
		return err
	}
	if err := unix.Fstat(int(state.guard.lock.Fd()), &l); err != nil {
		return err
	}
	if record.ProgramDev != uint64(p.Dev) || record.ProgramIno != uint64(p.Ino) || record.LeaseDev != uint64(l.Dev) || record.LeaseIno != uint64(l.Ino) || hex.EncodeToString(record.Nonce[:]) != state.guard.intent.Nonce || hex.EncodeToString(record.FSVerity[:]) != state.guard.item.FSVerity {
		return fmt.Errorf("VMM mode record differs from the original native capabilities")
	}
	return nil
}
