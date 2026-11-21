//go:build linux

package jail

import (
	"errors"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/iamlakshya/simstim/internal/proto"
	"golang.org/x/sys/unix"
)

const (
	vmmReadonlyControlEnterV1    byte = 5
	vmmReadonlyControlTerminalV1 byte = 6
	vmmReadonlyDiskAdoptedV1     byte = 7
	vmmReadonlyInitApprovalV1         = "VMMROINITACK0001"
	vmmReadonlyControlApprovalV1      = "VMMROCONTROLACK1"
	vmmReadonlyDiskLoanV1             = "VMMRODISK-000001"
	vmmReadonlyExecContinueV1         = "VMMROEXEC-000001"
)

func readReadonlyTaskLeafV1(actor *vmmExecActorV1, name string, limit int64) (_ []byte, returned error) {
	if err := actor.live(); err != nil {
		return nil, err
	}
	fd, err := unix.Openat(int(actor.task.Fd()), name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "original-vmm-task-leaf")
	defer func() { returned = errors.Join(returned, file.Close()) }()
	var fs unix.Statfs_t
	if err := unix.Fstatfs(fd, &fs); err != nil {
		return nil, err
	}
	if fs.Type != unix.PROC_SUPER_MAGIC {
		return nil, vmmModeFailureV1("the original VMM task leaf is not native procfs")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errors.Join(err, vmmModeFailureV1("the original VMM task leaf is unavailable or oversized"))
	}
	return data, actor.live()
}

func (owner *vmmExecOwnerV1) checkReadonlyMapsV1(actor *vmmExecActorV1) error {
	for _, entry := range []struct {
		name        string
		parent, sub uint32
		count       int
	}{{"uid_map", uint32(os.Getuid()), owner.subUID, 2}, {"gid_map", uint32(os.Getgid()), owner.readonlyMapSubGID, 2}} {
		if owner.readonlyLoginUID && entry.name == "uid_map" {
			entry.count = 1
		}
		data, err := readReadonlyTaskLeafV1(actor, entry.name, 4096)
		if err != nil {
			return err
		}
		if err := checkReadonlyMapRowsV1(data, entry.parent, entry.sub, entry.count); err != nil {
			return err
		}
	}
	return nil
}

func checkReadonlyMapRowsV1(data []byte, parent, sub uint32, count int) error {
	if count != 1 && count != 2 || count == 2 && parent == sub {
		return vmmModeFailureV1("the original read-only map has no distinct admitted IDs")
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != count {
		return vmmModeFailureV1("the original read-only VMM map differs from its actual UID or two-GID admission")
	}
	for index, line := range lines {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			return vmmModeFailureV1("the original read-only VMM map has another native shape")
		}
		outside := parent
		if index == 1 {
			outside = sub
		}
		for column, expected := range []uint64{uint64(index), uint64(outside), 1} {
			value, err := strconv.ParseUint(fields[column], 10, 32)
			if err != nil || value != expected {
				return vmmModeFailureV1("the read-only VMM map differs from its actual admitted creator")
			}
		}
	}
	return nil
}

func (owner *vmmExecOwnerV1) checkReadonlyIDsV1(actor *vmmExecActorV1, init bool) error {
	data, err := readReadonlyTaskLeafV1(actor, "status", 16<<10)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		key := fields[0]
		if key != "Uid:" && key != "Gid:" && key != "CapInh:" && key != "CapPrm:" && key != "CapEff:" && key != "CapBnd:" && key != "CapAmb:" && key != "NoNewPrivs:" {
			continue
		}
		if seen[key] {
			return vmmModeFailureV1("the original native VMM identity repeats a field")
		}
		seen[key] = true
		if key == "Uid:" || key == "Gid:" {
			if len(fields) != 5 {
				return vmmModeFailureV1("the original native VMM IDs have another shape")
			}
			wanted := [4]uint32{owner.subUID, owner.subUID, owner.subUID, owner.subUID}
			if key == "Gid:" {
				wanted = [4]uint32{owner.subGID, owner.subGID, owner.subGID, owner.subGID}
				if init {
					mismatch := uint32(os.Getgid())
					if owner.readonlyLoginUID {
						mismatch = owner.readonlyMapSubGID
					}
					wanted[1], wanted[3] = mismatch, mismatch
				}
			}
			for index, field := range fields[1:] {
				value, err := strconv.ParseUint(field, 10, 32)
				if err != nil || uint32(value) != wanted[index] {
					return vmmModeFailureV1("the actual read-only VMM actor changed its mapped native identity")
				}
			}
		} else {
			if len(fields) != 2 {
				return vmmModeFailureV1("the original VMM capability identity has another shape")
			}
			base, wanted := 16, uint64(0)
			if key == "NoNewPrivs:" {
				base, wanted = 10, 1
			}
			value, err := strconv.ParseUint(fields[1], base, 64)
			if err != nil || value != wanted {
				return vmmModeFailureV1("the actual read-only VMM actor retains capabilities or lacks NNP")
			}
		}
	}
	if len(seen) != 8 {
		return vmmModeFailureV1("the original native VMM identity is incomplete")
	}
	return nil
}

// Called under the SAME original owner's native-message lock. Metadata never
// chooses this branch: readonly was captured from that original store source.
func (owner *vmmExecOwnerV1) acceptReadonlyNativeV1(message vmmNativeMessageV1, actor *vmmExecActorV1, control *proto.UnixControlV1) error {
	if !owner.readonly || owner.readonlyPlan == nil || owner.rootNamespace || owner.init == nil {
		return vmmModeFailureV1("this original source has no closed read-only init route")
	}
	if owner.root == nil {
		return vmmModeFailureV1("the original read-only outside parent is missing")
	}
	if err := owner.root.live(); err != nil {
		return err
	}
	switch message.kind {
	case vmmReadonlyControlParentV1:
		if actor != owner.init || actor.stage != vmmStageInitV1 || !actor.entered || actor.issued || owner.root == nil || !owner.root.terminal || owner.readonlyParentApproved || len(control.Files) != 0 {
			return vmmModeFailureV1("the original read-only parent has another actual creation phase")
		}
		if err := owner.checkHelperActorV1(actor, actor.namespaces); err != nil {
			return err
		}
		if err := owner.checkReadonlyMapsV1(actor); err != nil {
			return err
		}
		if err := owner.checkReadonlyIDsV1(actor, true); err != nil {
			return err
		}
		owner.readonlyParentApproved = true
		return nil
	case vmmReadonlyControlEnterV1:
		if !owner.readonlyParentApproved || actor.stage != vmmStageExecV1 || actor.entered || owner.runner != nil || len(control.Files) != 4 {
			return vmmModeFailureV1("the original control child entered another native phase")
		}
		if err := owner.checkReadonlyMapsV1(actor); err != nil {
			return err
		}
		if err := owner.checkReadonlyIDsV1(actor, false); err != nil {
			return err
		}
		if err := actor.checkExecutableV1(owner.code); err != nil {
			return err
		}
		code, err := control.Files[0].Stat()
		if err != nil {
			return err
		}
		expected, err := owner.code.Stat()
		if err != nil {
			return err
		}
		if !os.SameFile(code, expected) {
			return vmmModeFailureV1("the control child supplied another native helper code")
		}
		for index, file := range control.Files[1:] {
			var fs unix.Statfs_t
			if err := unix.Fstatfs(int(file.Fd()), &fs); err != nil {
				return err
			}
			kind, err := unix.IoctlRetInt(int(file.Fd()), unix.NS_GET_NSTYPE)
			if err != nil {
				return err
			}
			if fs.Type != unix.NSFS_MAGIC || kind != []int{unix.CLONE_NEWUSER, unix.CLONE_NEWPID, unix.CLONE_NEWNS}[index] {
				return vmmModeFailureV1("the original control child namespace has another native role")
			}
			info, err := file.Stat()
			if err != nil {
				return err
			}
			if !os.SameFile(info, owner.init.namespaces[index]) {
				return vmmModeFailureV1("the control child changed its original init namespace")
			}
			actor.namespaces[index] = info
		}
		if err := actor.checkNamespacesV1(actor.namespaces); err != nil {
			return err
		}
		actor.entered, actor.stage = true, vmmStageRunnerV1
		owner.runner, owner.final = actor, owner.init
		owner.runnerOnce.Do(func() { close(owner.runnerReady) })
		close(owner.finalReady)
		return nil
	case vmmReadonlyControlTerminalV1:
		if actor != owner.runner || actor.terminal || owner.readonlyControlTerminal || len(control.Files) != 0 {
			return vmmModeFailureV1("the original control child has another no-exec boundary")
		}
		if err := owner.checkHelperActorV1(actor, actor.namespaces); err != nil {
			return err
		}
		if err := owner.checkReadonlyIDsV1(actor, false); err != nil {
			return err
		}
		actor.terminal, owner.readonlyControlTerminal = true, true
		close(owner.readonlyControlDone)
		return nil
	case vmmReadonlyDiskAdoptedV1:
		if actor != owner.init || !owner.readonlyDiskSent || owner.readonlyDiskAdopted || !owner.readonlyControlTerminal || owner.readonlyDisk == nil || len(control.Files) != 0 {
			return vmmModeFailureV1("the original init adopted no original disk loan")
		}
		if err := owner.checkReadonlyIDsV1(actor, true); err != nil {
			return err
		}
		if err := actor.checkReadonlyFDV1(8, owner.readonlyDisk); err != nil {
			return err
		}
		owner.readonlyDiskAdopted = true
		close(owner.readonlyProjectionReady)
		return nil
	}
	return vmmModeFailureV1("the original read-only source has another native message")
}

// Native reply readers are serialized: A consumed its terminal ACK before C's
// second mapping byte. B's first approval uses the EXISTING runner stream, so
// C alone receives disk and final continuation on the original creation socket.
func (owner *vmmExecOwnerV1) replyReadonlyNativeV1(kind byte) error {
	switch kind {
	case vmmReadonlyControlParentV1:
		_, err := owner.host.Write([]byte(vmmReadonlyInitApprovalV1))
		return err
	case vmmReadonlyControlEnterV1:
		owner.mu.Lock()
		init, connection := owner.init, owner.control
		owner.mu.Unlock()
		if init == nil || connection == nil {
			return vmmModeFailureV1("the original control approval lost its native init or stream")
		}
		if err := init.live(); err != nil {
			return err
		}
		return proto.SendUnixControlV1(connection, []byte(vmmReadonlyControlApprovalV1), []*os.File{init.process})
	case vmmReadonlyControlTerminalV1, vmmReadonlyDiskAdoptedV1:
		return nil
	}
	return vmmModeFailureV1("the original read-only reply has another native phase")
}
