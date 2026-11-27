//go:build linux

package jail

import (
	"context"
	"errors"
	"net"
	"os"

	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/supply"
	"golang.org/x/sys/unix"
)

// Parent identity comes from the SAME prepared factory descriptor, captured
// alongside the original Cmd's actual pinned bind. No path open occurs in S.
func (owner *vmmExecOwnerV1) checkReadonlyParentV2(parent *os.File) error {
	if owner == nil || !owner.readonly || owner.readonlyParent == nil || parent == nil {
		return vmmModeFailureV1("the read-only disk has no original native parent")
	}
	var actual, original unix.Stat_t
	if err := errors.Join(unix.Fstat(int(parent.Fd()), &actual), unix.Fstat(int(owner.readonlyParent.Fd()), &original)); err != nil {
		return err
	}
	flags, err := unix.FcntlInt(parent.Fd(), unix.F_GETFL, 0)
	if err != nil {
		return err
	}
	if actual.Dev != original.Dev || actual.Ino != original.Ino || actual.Mode&unix.S_IFMT != unix.S_IFDIR || actual.Mode&0o7777 != 0o700 || actual.Uid != owner.subUID || actual.Gid != uint32(os.Getgid()) || flags&unix.O_ACCMODE != unix.O_RDONLY || parent != owner.readonlyParent && flags&unix.O_PATH != 0 {
		return vmmModeFailureV1("the read-only disk parent differs from its original anchored native role")
	}
	return nil
}

func (owner *vmmExecOwnerV1) checkReadonlyDiskV2(disk, parent *os.File) error {
	if err := owner.checkReadonlyParentV2(parent); err != nil {
		return err
	}
	if disk == nil || owner.readonlyPlan == nil {
		return vmmModeFailureV1("the original read-only disk is absent")
	}
	var st unix.Stat_t
	if err := unix.Fstat(int(disk.Fd()), &st); err != nil {
		return err
	}
	flags, err := unix.FcntlInt(disk.Fd(), unix.F_GETFL, 0)
	if err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0o7777 != 0o600 || st.Uid != owner.subUID || st.Gid != owner.subGID || st.Nlink != 1 || st.Size != owner.readonlyPlan.DiskMax || flags&unix.O_ACCMODE != unix.O_RDWR || flags&unix.O_PATH != 0 {
		return vmmModeFailureV1("the original control child supplied another disk inode, rights or size")
	}
	if owner.readonlyDisk != nil {
		var original unix.Stat_t
		if err := unix.Fstat(int(owner.readonlyDisk.Fd()), &original); err != nil {
			return err
		}
		if original.Dev != st.Dev || original.Ino != st.Ino {
			return vmmModeFailureV1("the current original disk changed its received inode")
		}
	}
	return nil
}

// Returns both SAME detached actual objects even on any later error. The
// original phase adopts them before examining failure and joins physical use.
func (c *Cmd) LoanVMMReadonlyDiskV2(ctx context.Context, source *supply.ExecModeLeaseV1, connection *net.UnixConn, received *proto.UnixControlV1) (disk, parent *os.File, returned error) {
	owner, err := c.vmmOwnerV1(source)
	if err != nil {
		return nil, nil, err
	}
	if received == nil || len(received.Files) != 2 {
		return nil, nil, vmmModeFailureV1("the original control child omitted its disk/parent pair")
	}
	if err := received.CheckReceivedV1(connection); err != nil {
		return nil, nil, err
	}
	owner.mu.Lock()
	valid := owner.readonly && owner.control == connection && owner.runner != nil && owner.readonlyPlan != nil && !owner.readonlyDiskSent
	actor := owner.runner
	owner.mu.Unlock()
	if !valid {
		return nil, nil, vmmModeFailureV1("the original disk belongs to another read-only source")
	}
	if err := c.CheckVMMNativeSenderV1(source, connection, received.Credentials); err != nil {
		return nil, nil, err
	}
	if err := actor.checkExecutableV1(owner.code); err != nil {
		return nil, nil, err
	}
	files, err := received.DetachFilesV1()
	if err != nil {
		return nil, nil, err
	}
	disk, parent = files[0], files[1]
	if err := owner.checkReadonlyDiskV2(disk, parent); err != nil {
		return disk, parent, err
	}
	select {
	case <-ctx.Done():
		return disk, parent, ctx.Err()
	case <-owner.readonlyControlDone:
	case <-owner.nativeDone:
		return disk, parent, vmmModeFailureV1("the original control child has no native no-exec proof")
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.readonlyDiskSent || !owner.readonlyControlTerminal {
		return disk, parent, vmmModeFailureV1("the original disk loan is duplicate or premature")
	}
	if err := owner.init.checkExecutableV1(owner.code); err != nil {
		return disk, parent, err
	}
	owner.readonlyDisk, owner.readonlyDiskParent = disk, parent
	owner.readonlyDiskSent = true
	return disk, parent, proto.SendUnixControlV1(owner.host, []byte(vmmReadonlyDiskLoanV1), []*os.File{disk})
}

// This bounded current check is usable only from the actual original phase.
// Generic cachefs/native peer rules and all other purposes remain strict.
func (c *Cmd) CheckVMMReadonlyDiskV2(source *supply.ExecModeLeaseV1, disk, parent *os.File) error {
	owner, err := c.vmmOwnerV1(source)
	if err != nil {
		return err
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if !owner.readonly || !owner.finalProved || owner.readonlyPhysicalClosed || owner.readonlyDisk == nil || owner.runner == nil || owner.init == nil || owner.code == nil || owner.root == nil {
		return vmmModeFailureV1("the original received disk has no live final native phase")
	}
	if err := errors.Join(owner.root.live(), owner.runner.checkExecutableV1(owner.code), owner.runner.checkNamespacesV1(owner.runner.namespaces), owner.checkReadonlyIDsV1(owner.runner, false), owner.init.checkExecutableV1(owner.program)); err != nil {
		return err
	}
	return owner.checkReadonlyDiskV2(disk, parent)
}

// Each protected request consumes the actual current B SCM witness. Rights
// are checked, then its original receive owner closes those temporary loans.
func (c *Cmd) CheckVMMReadonlyDiskReplyV2(source *supply.ExecModeLeaseV1, connection *net.UnixConn, received *proto.UnixControlV1) error {
	if received == nil || len(received.Files) != 2 {
		return vmmModeFailureV1("the current disk reply omitted its native disk/parent pair")
	}
	if err := received.CheckReceivedV1(connection); err != nil {
		return err
	}
	if err := c.CheckVMMNativeSenderV1(source, connection, received.Credentials); err != nil {
		return err
	}
	return c.CheckVMMReadonlyDiskV2(source, received.Files[0], received.Files[1])
}
