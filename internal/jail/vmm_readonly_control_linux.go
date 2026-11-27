//go:build linux

package jail

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

const vmmReadonlyControlMarkerV1 = "--original-vmm-init-control-v1"
const vmmReadonlyControlParentV1 byte = 4

// This child belongs only to the closed original immutable-source migration.
// A marker, serialized setup or UIDMode cannot issue its native parent ACK.
// It uses the existing setup, creation, control and raw channel placements.
type vmmReadonlyControlChildV1 struct {
	parent      *setup
	setup       *setup
	command     *exec.Cmd
	extra       []*os.File
	setupWriter *os.File
	creation    *os.File // actual C Start-returned creation pidfd
	createdFD   int
	started     bool
}

// Prepare command data and inherited placement before C's irreversible final
// credential sequence. It creates no process and grants no release authority.
func prepareVMMReadonlyControlV1(c *setup) (*vmmReadonlyControlChildV1, error) {
	if c == nil || c.Sub == nil || os.Getpid() != 1 || unix.Gettid() != 1 {
		return nil, vmmModeFailureV1("read-only control needs the original two-ID VMM init")
	}
	if err := checkVMMLayoutV1(c); err != nil {
		return nil, err
	}
	data, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	var childSetup setup
	if err := json.Unmarshal(data, &childSetup); err != nil {
		return nil, err
	}
	child := &vmmReadonlyControlChildV1{parent: c, setup: &childSetup, extra: inherited(c, false), createdFD: -1}
	command := exec.Command(self)
	// No cancellation callback may release an unknown creation. The enclosing
	// original C/A/S physical owner performs cancellation and positive joins.
	command.Args = []string{self, "chv-run", vmmReadonlyControlMarkerV1, strconv.Itoa(3 + c.Files)}
	command.Env = []string{"LANG=C", "LC_ALL=C", "TZ=UTC"}
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	command.SysProcAttr = &syscall.SysProcAttr{PidFD: &child.createdFD}
	child.command = command
	return child, nil
}

// Start is the existing standard Go fork/exec, from C AFTER its actual fixed
// gid mismatch, UID1, caps0 and NNP. No raw fork/trampoline or retained generic
// capability crosses this helper exec. C's own IDs must remain unchanged.
// A nonnil child on error keeps every actual creation/setup object reachable.
func (child *vmmReadonlyControlChildV1) startV1() (_ *vmmReadonlyControlChildV1, returned error) {
	if child == nil || child.started || child.parent == nil || child.command == nil {
		return child, vmmModeFailureV1("read-only control lost its original unstarted constructor")
	}
	if err := checkVMMReadonlyControlParentV1(child.parent); err != nil {
		return child, err
	}
	// Only the same actual immutable-source owner can acknowledge this C.
	// No generic/writable source accepts the private read-only parent phase.
	if err := approveVMMReadonlyParentV1(child.parent); err != nil {
		return child, err
	}
	child.started = true
	writer, err := startWith(child.command, child.extra, 3+child.parent.Files)
	child.setupWriter = writer
	if child.createdFD >= 0 {
		child.creation = os.NewFile(uintptr(child.createdFD), "original-readonly-control-creation")
	}
	if err != nil {
		return child, err
	}
	if child.command.Process == nil || child.creation == nil {
		return child, vmmModeFailureV1("the original read-only control creation has no atomic handle")
	}
	pid, _, err := vmmPIDFDIdentityV1(child.creation)
	if err != nil || pid != child.command.Process.Pid {
		return child, errors.Join(err, vmmModeFailureV1("the control child differs from its actual atomic creation"))
	}
	if err := checkVMMReadonlyControlParentV1(child.parent); err != nil {
		return child, err
	}
	return child, nil
}

// Publish the SAME creation before setup release. The retained original handle
// stays with C; reportVMMCreation owns only a duplicate SCM loan. The source
// migration must next adopt B's one collapsed helper entry before disk access.
func (child *vmmReadonlyControlChildV1) publishAndReleaseV1() error {
	if child == nil || !child.started || child.creation == nil || child.setupWriter == nil {
		return vmmModeFailureV1("read-only control has no original blocked creation")
	}
	if err := checkVMMReadonlyControlParentV1(child.parent); err != nil {
		return err
	}
	loan, err := unix.FcntlInt(child.creation.Fd(), unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		return err
	}
	if err := reportVMMCreationV1(child.parent, stageInit, child.command.Process, loan); err != nil {
		return err
	}
	if err := writeSetup(child.setupWriter, child.setup); err != nil {
		return err
	}
	child.setupWriter = nil
	return checkVMMReadonlyControlParentV1(child.parent)
}

func checkVMMReadonlyControlParentV1(c *setup) error {
	if c == nil || c.Sub == nil || os.Getpid() != 1 || unix.Gettid() != 1 {
		return vmmModeFailureV1("read-only control is not the same original mapped namespace PID1")
	}
	if err := checkVMMLayoutV1(c); err != nil {
		return err
	}
	ruid, euid, suid := unix.Getresuid()
	rgid, egid, sgid := unix.Getresgid()
	uid, _ := readonlyExecutionIDsV1(c)
	groups := readonlyPreexecGroupsV1(c)
	if ruid != uid || euid != uid || suid != uid || rgid != int(groups[0]) || egid != int(groups[1]) || sgid != int(groups[2]) {
		return vmmModeFailureV1("control creation changed the original final-thread credential mismatch")
	}
	if err := checkVMMReadonlyCurrentIDsV1(uint64(uid), groups); err != nil {
		return err
	}
	if err := verifyInitPrivateStateV1(); err != nil {
		return err
	}
	nnp, err := unix.PrctlRetInt(unix.PR_GET_NO_NEW_PRIVS, 0, 0, 0, 0)
	if err != nil || nnp != 1 {
		return errors.Join(err, vmmModeFailureV1("read-only control creation lacks actual no-new-privileges"))
	}
	for capability := 0; capability < 64; capability++ {
		value, err := unix.PrctlRetInt(unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_IS_SET, uintptr(capability), 0, 0)
		if errors.Is(err, unix.EINVAL) {
			break
		}
		if err != nil || value != 0 {
			return errors.Join(err, vmmModeFailureV1("read-only control creation retains ambient capabilities"))
		}
	}
	return nil
}

func checkVMMReadonlyCurrentIDsV1(uid uint64, groups [4]uint64) (returned error) {
	file, err := os.Open("/proc/thread-self/status")
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, file.Close()) }()
	var filesystem unix.Statfs_t
	if err := unix.Fstatfs(int(file.Fd()), &filesystem); err != nil {
		return err
	}
	if filesystem.Type != unix.PROC_SUPER_MAGIC {
		return vmmModeFailureV1("the original final-thread identity is not native procfs")
	}
	data, err := io.ReadAll(io.LimitReader(file, (16<<10)+1))
	if err != nil || len(data) > 16<<10 {
		return errors.Join(err, vmmModeFailureV1("the original final-thread identity is oversized or unavailable"))
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != "Uid:" && fields[0] != "Gid:" {
			continue
		}
		if seen[fields[0]] || len(fields) != 5 {
			return vmmModeFailureV1("the original final-thread IDs have another native shape")
		}
		seen[fields[0]] = true
		wanted := [4]uint64{uid, uid, uid, uid}
		if fields[0] == "Gid:" {
			wanted = groups
		}
		for index, text := range fields[1:] {
			value, err := strconv.ParseUint(text, 10, 32)
			if err != nil || value != wanted[index] {
				return vmmModeFailureV1("the original final-thread real, effective, saved or filesystem ID changed")
			}
		}
	}
	if !seen["Uid:"] || !seen["Gid:"] {
		return vmmModeFailureV1("the original final-thread identity is incomplete")
	}
	return nil
}
