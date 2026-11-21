package jail

import (
	"errors"
	"net"
	"os"
	"strconv"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

// gvisorNestedCreationV1 is custody of C's SAME atomic creation descriptor.
// It is issued only on this Cmd's original mapped A control. It proves no
// runtime namespace, final filesystem, aggregate or private-input permission.
// A pending value deliberately cannot be joined or converted into readiness.
type gvisorNestedCreationV1 struct {
	mu                            sync.Mutex
	original                      *gvisorNestedCreationV1
	outer                         *gvisorOuterOwnerV1
	creation, task, creator, code *os.File
	birth                         gvisorTaskV2
}

func emptyGvisorControlV1(message gvisorMessageV2) bool {
	return message.SlotCount == 0 && message.SocketFD == 0 && message.Setup == nil && len(message.Slots) == 0
}

// This call follows ONLY the actual synchronous StartProcess(self, fixed map
// argv, fixed namespace attrs, harmless null vector) above. The exact Process
// and original creation file remain with A until its own positive wait/reap.
func reportGvisorNestedCreationV1(c *setup, child *os.Process, creation *os.File) (failure error) {
	if c == nil || c.gvisorReport == nil || child == nil || creation == nil || !c.gvisorOuterNative || c.GvisorV1Version != 1 {
		return gvisorHandoffFailureV2("the nested creation has no original outside report source")
	}
	control := c.gvisorReport
	defer func() {
		failure = errors.Join(failure, control.Close())
		c.gvisorReport = nil
		if c.gvisorForkRoot != nil {
			failure = errors.Join(failure, c.gvisorForkRoot.Close())
			c.gvisorForkRoot = nil
		}
	}()
	if err := checkGvisorMountedRootV2(c.gvisorForkRoot); err != nil {
		return err
	}
	task, err := openGvisorProcV2("/proc/" + strconv.Itoa(child.Pid))
	if err != nil {
		return err
	}
	defer task.Close()
	creator, err := openGvisorProcV2("/proc/self")
	if err != nil {
		return err
	}
	defer creator.Close()
	code, err := os.Open(self)
	if err != nil {
		return err
	}
	defer code.Close()
	if err = checkNativeLivePIDFDV1(int(creation.Fd())); err != nil {
		return err
	}
	if err = sendGvisorV2(control, gvisorMessageV2{Phase: "nested-created-v2"}, []*os.File{creation, task, creator, code, c.gvisorForkRoot}); err != nil {
		return err
	}
	message, _, err := receiveGvisorV2(control, unix.Ucred{Pid: 0, Uid: 0, Gid: 0}, "nested-retained-v2", 0)
	if err != nil {
		return err
	}
	if !emptyGvisorControlV1(message) {
		return gvisorHandoffFailureV2("the original nested creation acknowledgement changed roles")
	}
	return checkNativeLivePIDFDV1(int(creation.Fd()))
}

func nativeGvisorTaskV1(file *os.File) (gvisorTaskV2, unix.Stat_t, error) {
	var st unix.Stat_t
	var fs unix.Statfs_t
	if file == nil {
		return gvisorTaskV2{}, st, gvisorHandoffFailureV2("a nested creation task object is absent")
	}
	if err := errors.Join(unix.Fstat(int(file.Fd()), &st), unix.Fstatfs(int(file.Fd()), &fs)); err != nil {
		return gvisorTaskV2{}, st, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR || fs.Type != unix.PROC_SUPER_MAGIC {
		return gvisorTaskV2{}, st, gvisorHandoffFailureV2("a nested creation task is not a native process directory")
	}
	raw, err := readGvisorAtV2(file, "stat")
	if err != nil {
		return gvisorTaskV2{}, st, err
	}
	task, err := parseGvisorTaskV2(raw)
	return task, st, err
}

func checkGvisorNestedTasksV1(local, creator, outside, originalA gvisorTaskV2, pid int, pids []int) error {
	if pid <= 0 || len(pids) != 3 || pids[0] != pid || pids[1] != local.pid || pids[2] != 1 || local.pid <= 1 || local.parent != 1 || creator.pid != 1 || creator.parent != 0 || creator.birth != originalA.birth || outside.pid != pid || outside.parent != originalA.pid || outside.birth != local.birth {
		return gvisorHandoffFailureV2("the nested original pidfd, native birth and mapped creator chain differ")
	}
	return nil
}

// The CURRENT SCM sender is still exact original A. Kernel pidfd fdinfo gives
// the task number in the outside view; it does not adopt an advertised PID.
// Both native process views must agree on birth and the original creator.
func (c *Cmd) receiveGvisorNestedCreationV1(control *net.UnixConn, peer unix.Ucred, actualA *os.File, originalA gvisorTaskV2) (failure error) {
	c.mu.Lock()
	owner := c.gvisorOuter
	var nested *gvisorNestedCreationV1
	if owner != nil {
		nested = owner.nested
	}
	c.mu.Unlock()
	if nested == nil || peer.Pid != int32(originalA.pid) || originalA.pid <= 0 {
		return gvisorHandoffFailureV2("the original pending nested creation owner changed")
	}
	nested.mu.Lock()
	pending := nested.outer == owner && nested.original == nil
	nested.mu.Unlock()
	if !pending {
		return gvisorHandoffFailureV2("the original pending nested creation was already issued")
	}
	message, files, err := receiveGvisorV2(control, peer, "nested-created-v2", 5)
	if err != nil {
		return err
	}
	retained := false
	defer func() {
		if !retained {
			closeFiles(files)
		}
	}()
	if !emptyGvisorControlV1(message) {
		return gvisorHandoffFailureV2("the nested creation report contains another role or setup")
	}
	pids, err := gvisorTransferredPIDsV1(files[0])
	if err != nil {
		return err
	}
	pid := pids[0]
	local, localStat, err := nativeGvisorTaskV1(files[1])
	if err != nil {
		return err
	}
	creator, creatorStat, err := nativeGvisorTaskV1(files[2])
	if err != nil {
		return err
	}
	outside, err := openGvisorProcV2("/proc/" + strconv.Itoa(pid))
	if err != nil {
		return err
	}
	defer outside.Close()
	current, _, err := nativeGvisorTaskV1(outside)
	if err != nil {
		return err
	}
	if localStat.Dev != creatorStat.Dev || localStat.Ino == creatorStat.Ino {
		return gvisorHandoffFailureV2("the mapped nested proof substituted its native process view")
	}
	if err = checkGvisorNestedTasksV1(local, creator, current, originalA, pid, pids); err != nil {
		return err
	}
	actualCode, err := files[3].Stat()
	expectedCode, expectedErr := owner.helper.Stat()
	flags, flagsErr := unix.FcntlInt(files[3].Fd(), unix.F_GETFL, 0)
	if err != nil || expectedErr != nil || flagsErr != nil || !os.SameFile(actualCode, expectedCode) || flags&unix.O_ACCMODE != unix.O_RDONLY || flags&unix.O_PATH != 0 {
		return errors.Join(err, expectedErr, flagsErr, gvisorHandoffFailureV2("the nested report did not retain the original mapped helper code"))
	}
	actual, _, err := nativeGvisorTaskV1(actualA)
	if err != nil || actual != originalA {
		return errors.Join(err, gvisorHandoffFailureV2("the original A sender changed during nested custody"))
	}
	currentLocal, _, err := nativeGvisorTaskV1(files[1])
	if err != nil || currentLocal != local {
		return errors.Join(err, gvisorHandoffFailureV2("the nested task changed during original creation custody"))
	}
	if err = checkNativeLivePIDFDV1(int(files[0].Fd())); err != nil {
		return err
	}
	// Publish custody BEFORE cancellation, a byte recheck or the acknowledgement
	// can fail. No subsequent error discards this actual creation descriptor.
	nested.mu.Lock()
	nested.creation, nested.task, nested.creator, nested.code, nested.birth = files[0], files[1], files[2], files[3], local
	nested.original = nested
	nested.mu.Unlock()
	retained = true
	if owner.fork == nil {
		return errors.Join(gvisorHandoffFailureV2("the actual original A report has no pending mounted-root receiver"), files[4].Close())
	}
	if err := owner.fork.acceptRoot(files[4]); err != nil {
		return err
	}
	identity, err := gvisorCodeClosureV1(c.ctx, owner.launcher, owner.helper)
	if err != nil || identity != owner.bootstrapIdentity {
		return errors.Join(err, gvisorHandoffFailureV2("the original captured bootstrap changed before nested custody acknowledgement"))
	}
	return sendGvisorV2(control, gvisorMessageV2{Phase: "nested-retained-v2"}, nil)
}

// mu protects original custody publication and every field reader. A blocking
// positive wait borrows the retained FD after the check and releases mu first,
// so a concurrent cancellation can still signal that SAME actual process.
func (nested *gvisorNestedCreationV1) checkLockedV1() error {
	if nested == nil || nested.original != nested || nested.outer == nil || nested.creation == nil || nested.task == nil || nested.creator == nil || nested.code == nil || nested.birth.pid <= 1 {
		return gvisorHandoffFailureV2("the original nested C creation lifetime is incomplete")
	}
	return nil
}
func (nested *gvisorNestedCreationV1) checkV1() error {
	if nested == nil {
		return gvisorHandoffFailureV2("the original nested C creation lifetime is absent")
	}
	nested.mu.Lock()
	defer nested.mu.Unlock()
	return nested.checkLockedV1()
}
func (nested *gvisorNestedCreationV1) liveV1() error {
	if nested == nil {
		return nested.checkV1()
	}
	nested.mu.Lock()
	defer nested.mu.Unlock()
	if err := nested.checkLockedV1(); err != nil {
		return err
	}
	if err := checkNativeLivePIDFDV1(int(nested.creation.Fd())); err != nil {
		return err
	}
	current, _, err := nativeGvisorTaskV1(nested.task)
	if err != nil || current != nested.birth {
		return errors.Join(err, gvisorHandoffFailureV2("the original nested C native birth or parent changed"))
	}
	return nil
}
func (nested *gvisorNestedCreationV1) signalV1(signal syscall.Signal) error {
	if nested == nil {
		return nested.checkV1()
	}
	nested.mu.Lock()
	defer nested.mu.Unlock()
	if err := nested.checkLockedV1(); err != nil {
		return err
	}
	err := ownedPidfdSignal(int(nested.creation.Fd()), signal, nil, 0)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	return err
}
func (nested *gvisorNestedCreationV1) readyLockedV1() error {
	if err := nested.checkLockedV1(); err != nil {
		return err
	}
	ready, err := ownedPIDFDReady(int(nested.creation.Fd()), 0)
	if err != nil || !ready {
		return errors.Join(err, gvisorHandoffFailureV2("the original nested C has no positive native exit"))
	}
	return nil
}
func (nested *gvisorNestedCreationV1) readyV1() error {
	if nested == nil {
		return nested.checkV1()
	}
	nested.mu.Lock()
	defer nested.mu.Unlock()
	return nested.readyLockedV1()
}
func (nested *gvisorNestedCreationV1) joinV1() error {
	if nested == nil {
		return nested.checkV1()
	}
	nested.mu.Lock()
	if err := nested.checkLockedV1(); err != nil {
		nested.mu.Unlock()
		return err
	}
	fd := int(nested.creation.Fd())
	nested.mu.Unlock()
	return waitOwnedPIDFD(fd, -1)
}
func (nested *gvisorNestedCreationV1) closeJoinedV1() error {
	if nested == nil {
		return nested.checkV1()
	}
	nested.mu.Lock()
	defer nested.mu.Unlock()
	if err := nested.readyLockedV1(); err != nil {
		return err
	}
	return errors.Join(nested.creation.Close(), nested.task.Close(), nested.creator.Close(), nested.code.Close())
}
