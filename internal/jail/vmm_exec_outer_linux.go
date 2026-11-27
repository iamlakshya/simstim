//go:build linux

package jail

import (
	"errors"
	"os"
	"os/exec"
	"reflect"
	"runtime"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

// This captures the actual creator OS thread before its synchronous self-exec.
// It is not an issuer: only the real post-Start atomic publication below can
// turn these original native handles into a receipt for that exact child.
type vmmOuterCreatorV1 struct{ namespaces [3]*os.File }

// vmmOuterExecReceiptV1 has no constructor accepting metadata or a boolean.
// vmmCreatedRootV1 alone issues it from the original private Cmd after the
// actual synchronous /proc/self/exe execution and atomic creation publication.
// That closed outer path performs no exec or namespace transition itself:
// it creates the mapped init, forwards signals and reaps. Its actual SCM and
// native birth must still agree at each original entry/terminal boundary.
type vmmOuterExecReceiptV1 struct {
	command *exec.Cmd
	process *os.Process
	actor   *vmmExecActorV1
	code    *os.File
	creator *vmmOuterCreatorV1
}

func checkVMMOuterAttrsV1(attr *syscall.SysProcAttr, createdFD int, started bool) error {
	if attr == nil {
		return vmmModeFailureV1("the original outer has no closed native creation attributes")
	}
	actual := *attr
	if started {
		if actual.PidFD == nil || *actual.PidFD != createdFD || createdFD < 0 {
			return vmmModeFailureV1("the original outer lost its actual atomic creation result")
		}
	} else if actual.PidFD != nil {
		return vmmModeFailureV1("the original outer creation already has another issuer")
	}
	actual.PidFD = nil
	if !reflect.DeepEqual(actual, syscall.SysProcAttr{Setsid: true}) {
		return vmmModeFailureV1("the original outer changes credentials, namespaces or another closed creation property")
	}
	return nil
}
func checkVMMOuterCommandV1(c *Cmd, createdFD int, started bool) error {
	if c == nil || c.cmd == nil || c.cmd.Path != self || c.cmd.Dir != "" || c.cmd.Env == nil || len(c.cmd.Env) != 0 || len(c.cmd.Args) != 3 || c.cmd.Args[0] != argv0 || c.cmd.Args[1] != stageOuterVMMV1 || c.cmd.Args[2] != strconv.Itoa(3+len(c.files)) {
		return vmmModeFailureV1("the original outer is not the fixed direct self-exec command")
	}
	return checkVMMOuterAttrsV1(c.cmd.SysProcAttr, createdFD, started)
}
func openVMMCreatorNamespaceV1(index int) (*os.File, error) {
	names := []string{"user", "pid", "mnt"}
	file, err := os.Open("/proc/thread-self/ns/" + names[index])
	if err != nil {
		return nil, err
	}
	var fs unix.Statfs_t
	if err := unix.Fstatfs(int(file.Fd()), &fs); err != nil || fs.Type != unix.NSFS_MAGIC {
		file.Close()
		return nil, errors.Join(err, vmmModeFailureV1("the actual creator namespace is not native nsfs"))
	}
	kind, err := unix.IoctlRetInt(int(file.Fd()), unix.NS_GET_NSTYPE)
	if err != nil || kind != []int{unix.CLONE_NEWUSER, unix.CLONE_NEWPID, unix.CLONE_NEWNS}[index] {
		file.Close()
		return nil, errors.Join(err, vmmModeFailureV1("the actual creator namespace has another native role"))
	}
	return file, nil
}

// Caller holds the original mode-owner mutex. The sole actual Start defer
// releases this SAME goroutine's lock on every success/error path.
func (owner *vmmExecOwnerV1) captureOuterCreatorV1(c *Cmd) error {
	if owner.rootNamespace {
		return nil
	}
	if owner.outerCreator != nil {
		return vmmModeFailureV1("the original outer creator thread was already captured")
	}
	if err := checkVMMOuterCommandV1(c, -1, false); err != nil {
		return err
	}
	runtime.LockOSThread()
	owner.creationThreadLocked = true
	creator := &vmmOuterCreatorV1{}
	owner.outerCreator = creator
	for index := range creator.namespaces {
		file, err := openVMMCreatorNamespaceV1(index)
		if err != nil {
			return err
		}
		creator.namespaces[index] = file
	}
	return nil
}

// releaseVMMCreationThreadV1 is called only on the original Start goroutine.
// It changes scheduling only; it provides no execution or cleanup permission.
func (c *Cmd) releaseVMMCreationThreadV1() {
	if c == nil || c.modeExecV1 == nil {
		return
	}
	owner := c.modeExecV1
	owner.mu.Lock()
	locked := owner.creationThreadLocked
	owner.creationThreadLocked = false
	owner.mu.Unlock()
	if locked {
		runtime.UnlockOSThread()
	}
}
func (owner *vmmExecOwnerV1) issueOuterExecReceiptV1(c *Cmd, actor *vmmExecActorV1, createdFD int) error {
	if owner == nil || c == nil || actor == nil || owner.code == nil || owner.source == nil || c.modeExecV1 != owner || actor.process == nil || actor.task == nil {
		return vmmModeFailureV1("the original outer receipt has no actual native constructor")
	}
	if owner.rootNamespace || owner.outerCreator == nil || !owner.creationThreadLocked || owner.outerReceipt != nil {
		return vmmModeFailureV1("the original outer has no unique actual creator-thread capture")
	}
	if err := checkVMMOuterCommandV1(c, createdFD, true); err != nil {
		return err
	}
	if c.cmd.Process == nil || c.cmd.Process.Pid != actor.pid {
		return vmmModeFailureV1("the original outer differs from the actual synchronous creation")
	}
	current, err := os.Stat(self)
	if err != nil {
		return err
	}
	expected, err := owner.code.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(current, expected) {
		return vmmModeFailureV1("the synchronous outer self-exec changed its actual parent executable")
	}
	for index, original := range owner.outerCreator.namespaces {
		if original == nil {
			return vmmModeFailureV1("the original creator namespace source is incomplete")
		}
		current, err := openVMMCreatorNamespaceV1(index)
		if err != nil {
			return err
		}
		actual, actualErr := current.Stat()
		expected, expectedErr := original.Stat()
		closeErr := current.Close()
		if actualErr != nil || expectedErr != nil || closeErr != nil {
			return errors.Join(actualErr, expectedErr, closeErr)
		}
		if !os.SameFile(actual, expected) {
			return vmmModeFailureV1("the locked actual creator thread changed native namespace during its self-exec")
		}
	}
	if err := actor.live(); err != nil {
		return err
	}
	owner.outerReceipt = &vmmOuterExecReceiptV1{command: c.cmd, process: c.cmd.Process, actor: actor, code: owner.code, creator: owner.outerCreator}
	return nil
}
func (owner *vmmExecOwnerV1) checkHelperActorV1(actor *vmmExecActorV1, namespaces [3]os.FileInfo) error {
	if owner == nil || actor == nil {
		return vmmModeFailureV1("the actual helper actor is missing")
	}
	receipt := owner.outerReceipt
	if receipt == nil || actor != receipt.actor {
		if err := actor.checkExecutableV1(owner.code); err != nil {
			return err
		}
		return actor.checkNamespacesV1(namespaces)
	}
	// This is an explicit source class issued by actual native creation, not
	// a fallback after a failed task/exe or task/ns open. No other actor can use it.
	if owner.rootNamespace || actor != owner.root || actor.stage != vmmStageOuterV1 || receipt.command == nil || receipt.command.Process != receipt.process || receipt.process == nil || receipt.process.Pid != actor.pid || receipt.code != owner.code || receipt.creator != owner.outerCreator {
		return vmmModeFailureV1("the native outer receipt belongs to another original creation")
	}
	if err := actor.live(); err != nil {
		return err
	}
	for index, file := range receipt.creator.namespaces {
		actual, err := file.Stat()
		if err != nil {
			return err
		}
		if namespaces[index] == nil || !os.SameFile(actual, namespaces[index]) {
			return vmmModeFailureV1("the actual outer SCM namespace source differs from its closed native self-exec")
		}
	}
	return actor.live()
}
