package jail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"os/user"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/proto"
	"golang.org/x/sys/unix"
)

// self is the running program, which every stage of a jail starts again.
// The link holds even when an upgrade replaced the file.
const self = "/proc/self/exe"

// argv0 marks a process that Init must turn into a stage of a jail. The
// stage's name follows it, then the descriptor of its set-up.
var argv0 = brand.Name + "-jail"

// The stages of a jail. A jail runs as outer (for a subuid only), then
// init, then exec, which becomes the command.
const (
	stageOuter  = "outer"  // outside, for a subuid: start init, map its IDs, wait for it
	stageInit   = "init"   // build the root, confine itself, start exec, reap, end the rest
	stageExec   = "exec"   // add the command's own Landlock rules, then become the command
	stageProbe  = "probe"  // in the namespaces: build an empty root, then exit
	stageRemove = "remove" // in a user namespace with the subuid mapped: remove a folder
)

// setup is what a stage needs. It reaches each stage on a pipe. Its
// descriptor numbers are the same in every stage.
type setup struct {
	ControllerPublicV1  *mappedControllerPublicV1 `json:"controller_public_v1,omitempty"`
	PublicationLeaseV1  *publicationLeaseSetupV1  `json:"publication_lease_v1,omitempty"`
	ReadonlyVMMInitV1   *VMMReadonlyInitPlanV1    `json:"readonly_vmm_init_v1,omitempty"`
	VMMExecV1           *vmmExecSetupV1           `json:"vmm_exec_v1,omitempty"`
	InitLeaseV1Version  int                       `json:"init_lease_v1_version,omitempty"`
	InitLeaseFDV1       int                       `json:"init_lease_fd_v1,omitempty"`
	InitLeaseRefV1      *slotInitLeaseRefV1       `json:"init_lease_ref_v1,omitempty"`
	gvisorCommonScope   *gvisorCommonScopeV1      // actual scope-only native enforcement on A
	gvisorOuterGate     *os.File                  // original C death gate retained only by A
	gvisorOuterNative   bool                      // issued only by the fixed early outer C/Go handshake
	gvisorCreationChild *os.Process               // same original unreaped C task
	gvisorCreation      *os.File                  // A retains its exact C creation pidfd through reap.
	gvisorReport        *net.UnixConn             // SAME original post-map A/outside control; never serialised or inherited.
	GvisorV1Version     int                       `json:"gvisor_v1_version,omitempty"`
	BindsV1Version      int                       `json:"binds_v1_version,omitempty"`
	FilesV1Version      int                       `json:"files_v1_version,omitempty"`
	FileRulesV1         []pinnedFileRuleV1        `json:"file_rules_v1,omitempty"`
	Argv                []string                  `json:"argv"`
	ExecFD              int                       `json:"exec_fd,omitempty"` // Argv[0] is /proc/self/fd/<ExecFD>, one of Files
	Binds               []bind                    `json:"binds,omitempty"`
	Rules               []rule                    `json:"rules,omitempty"`
	Net                 NetMode                   `json:"net"`
	Seccomp             Profile                   `json:"seccomp"`
	Files               int                       `json:"files"`               // inherited files, from fd 3
	StatusFD            int                       `json:"status_fd,omitempty"` // init says here how the command ended
	SyncFD              int                       `json:"sync_fd,omitempty"`   // init or remove waits here for outer's ID maps
	Userns              bool                      `json:"userns,omitempty"`    // the jail has namespaces
	Sub                 *subID                    `json:"sub,omitempty"`       // run the command as a subuid
	Remove              string                    `json:"remove,omitempty"`    // the folder that the remove stage removes
	gvisorForkRoot      *os.File                  // actual post-buildRoot A root; parent-only report role, never C Files
}

// Test hooks: whether a user namespace works, the Landlock ABI, and how the
// probe starts.
var (
	usernsWorks = usernsState
	probeFn     = probeUserns
	landlockABI = kernelABI
	probeFlags  uintptr           // more clone flags for the probe
	probeVars   []string          // the probe's environment
	nsDir       = "/proc/self/ns" // one file for each namespace that the kernel knows
	maxUserNS   = "/proc/sys/user/max_user_namespaces"
)

// stageHook, when a test sets it, runs beside the outer stage's set-up.
var stageHook func()

// initDone says that this program calls Init, so its stages can run.
var initDone atomic.Bool

// Init runs a stage of a jail when this process is one, and never returns
// then. Otherwise it returns at once. A program that calls Command calls
// Init first in main, and a test binary calls it from TestMain.
func Init() {
	initDone.Store(true)
	if len(os.Args) < 2 || os.Args[0] != argv0 {
		if err := captureVMMReadonlyControlStageV1(); err != nil {
			os.Exit(stageFailed(err))
		}
		if err := captureVMMRunnerStageV1(); err != nil {
			os.Exit(stageFailed(err))
		}
		return
	}
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		os.Exit(stageFailed(failure("cannot protect the jail before reading its set-up: "+err.Error(), "Check this host's process confinement support.")))
	}
	os.Exit(runStage(os.Args[1:]))
}

// Command returns a command that starts argv inside the jail. Every jail
// also sets no_new_privs, and drops all capabilities before it runs argv.
// execve sets the dumpable flag to 1 again, so a program of the helper,
// such as egress, sets PR_SET_DUMPABLE to 0 itself before it reads fd 3.
//
// Command pins every bound path and every rule's path. The command starts
// in a new session, as the child of a small init that the jail confines too.
// The init passes on SIGTERM, SIGINT, SIGHUP, SIGQUIT, SIGUSR1, SIGUSR2 and
// SIGWINCH, reaps orphans, and ends every process of the jail when the
// command ends. A jail that cannot be built exits with code 125 before argv
// runs, and says why on stderr. Cancelling ctx kills the whole jail.
func Command(ctx context.Context, s Spec) (*Cmd, error) {
	if !initDone.Load() {
		return nil, failure("this program cannot start jails, because it does not call jail.Init",
			"Call jail.Init first in main, or in TestMain.")
	}
	c, err := check(s)
	if err != nil {
		return nil, err
	}
	fileRules, err := snapshotFileRulesV1(c.fileRulesV1, s.Files)
	if err != nil {
		return nil, err
	}
	fd, err := openPath(unix.AT_FDCWD, "/", unix.RESOLVE_NO_MAGICLINKS)
	if err != nil {
		return nil, pinError("cannot check the jail's path support", "Check that this host supports openat2.", err)
	}
	_ = unix.Close(fd)
	m := mode{userns: true}
	if !s.gvisorV1 {
		userns, err := usernsWorks()
		if err != nil {
			return nil, err
		}
		m, err = choose(s, userns, landlockABI(), lookupSubID)
		if err != nil {
			return nil, err
		}
	}
	p, err := pin(c, len(s.Files))
	if err != nil {
		return nil, err
	}
	set := setup{Argv: c.argv, ExecFD: c.execFD, Binds: p.binds, Rules: p.rules, Net: s.Net, Seccomp: s.Seccomp,
		Files: len(s.Files), StatusFD: 4 + len(s.Files), Userns: m.userns, Sub: m.sub, FileRulesV1: fileRules}
	if len(fileRules) != 0 {
		set.FilesV1Version = 1
	}
	if slices.ContainsFunc(p.binds, func(b bind) bool { return b.SuppliedV1 }) {
		set.BindsV1Version = 1
	}
	if s.gvisorV1 {
		set.GvisorV1Version = 1
	}
	b, err := json.Marshal(set)
	if err != nil {
		p.close()
		return nil, failure("cannot write the jail's set-up: "+err.Error(), "Report this as a bug.")
	}
	stage := stageInit
	attr := &syscall.SysProcAttr{Setsid: true}
	switch {
	case m.sub != nil:
		stage = stageOuter
	case m.userns:
		attr = namespaces(s.Net, true, initCaps)
		if s.gvisorV1 {
			attr.Pdeathsig = unix.SIGKILL
			attr.UidMappings = []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}}
			attr.GidMappings = []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}}
		}
		attr.Setsid = true
	}
	cmd := exec.CommandContext(context.WithoutCancel(ctx), self)
	cmd.Args = []string{argv0, fileRulesStageV1(stage, &set), strconv.Itoa(3 + len(s.Files))}
	cmd.Env = append([]string{}, s.Env...)
	cmd.SysProcAttr = attr
	return &Cmd{ctx: ctx, cmd: cmd, setup: b, files: slices.Clone(s.Files), fileRulesV1: fileRules, binds: p.binds, rules: p.rules, pins: p.files,
		done: make(chan struct{}), abort: make(chan struct{})}, nil
}

// initCaps are the capabilities that init keeps across its execve in the
// new user namespace: mounts and pivot_root, the bounding set, the subuid,
// and the owner of a writable bind. It drops them all before it starts the
// command.
var initCaps = []uintptr{unix.CAP_SYS_ADMIN, unix.CAP_SETPCAP, unix.CAP_SETUID, unix.CAP_SETGID, unix.CAP_CHOWN}

// namespaces returns the attributes that start a stage in a jail's
// namespaces. With idMap, Go maps the login user and group to themselves.
// Without it, the outer stage maps them with newuidmap and newgidmap.
//
// Nothing sets Credential, so Go never calls setgroups: the command keeps
// the login user's groups, such as kvm (isolation spec 14.4.2). With a map,
// Go writes "deny" to setgroups, so nothing in the jail can call it either.
func namespaces(n NetMode, idMap bool, caps []uintptr) *syscall.SysProcAttr {
	flags := uintptr(unix.CLONE_NEWUSER | unix.CLONE_NEWNS | unix.CLONE_NEWPID |
		unix.CLONE_NEWIPC | unix.CLONE_NEWUTS | unix.CLONE_NEWCGROUP)
	if n == NetNone {
		flags |= unix.CLONE_NEWNET
	}
	attr := &syscall.SysProcAttr{Cloneflags: flags, AmbientCaps: caps}
	if idMap {
		uid, gid := os.Getuid(), os.Getgid()
		attr.UidMappings = []syscall.SysProcIDMap{{ContainerID: uid, HostID: uid, Size: 1}}
		attr.GidMappings = []syscall.SysProcIDMap{{ContainerID: gid, HostID: gid, Size: 1}}
	}
	return attr
}

var (
	probeMu sync.Mutex
	probed  *bool // the last clear answer of the probe
)

// Userns says whether jails on this host get a user namespace: directly,
// or through the namespace launcher (14.4.2). It finds out by starting this
// program in the namespaces that a jail uses, where it builds an empty root
// as a jail does. A user namespace without capabilities, as Ubuntu gives an
// unconfined program, fails that. Only a clear answer is kept: a probe that
// times out, or finds the host short of processes or memory, runs again
// next time. A program that does not call Init gets false.
func Userns() bool {
	ok, _ := usernsState()
	return ok
}

// usernsState is Userns, with the error of a probe that gave no clear answer.
func usernsState() (bool, error) {
	if !initDone.Load() {
		return false, nil
	}
	probeMu.Lock()
	defer probeMu.Unlock()
	if probed != nil {
		return *probed, nil
	}
	ok, err := probeFn()
	if err != nil {
		return false, err
	}
	probed = &ok
	slog.Info("jails on this host", "userns", ok, "landlock_abi", kernelABI())
	return ok, nil
}

// Exit codes of the probe stage.
const (
	probeRefused = 1 // the kernel refused: a clear no
	probeUnclear = 2 // anything else, such as a host short of memory
)

func probeUserns() (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, self)
	cmd.Args = []string{argv0, stageProbe}
	cmd.Env = append([]string{}, probeVars...)
	cmd.SysProcAttr = namespaces(NetNone, true, initCaps)
	cmd.SysProcAttr.Cloneflags |= probeFlags
	err := cmd.Run()
	return classifyProbe(err, ctx.Err() != nil)
}

// classifyProbe turns the probe's end into an answer. Only a clear answer
// is one: the probe built its root, or the kernel refused. It refuses with
// EPERM or EACCES, to clone the namespaces (a sysctl, AppArmor, a uid_map
// write) or in the probe (exit probeRefused). It refuses with ENOSPC from
// clone only when max_user_namespaces is 0, and with EINVAL from clone only
// for a namespace that it does not know, and from pivot_root only on a
// rootfs root. Anything else, such as a timeout, a signal, another ENOSPC
// or EINVAL, EMFILE, ENFILE, EAGAIN or ENOMEM, is an error that Command
// passes on, and the next jail probes again.
func classifyProbe(err error, timedOut bool) (bool, error) {
	var ee *exec.ExitError
	switch {
	case err == nil:
		return true, nil
	case timedOut:
		return false, failure("the check for user namespaces timed out", "Try again when the host is less busy.")
	case errors.As(err, &ee) && ee.ExitCode() == probeRefused:
		return false, nil
	case errors.As(err, &ee):
		return false, failure("the check for user namespaces ended without an answer: "+err.Error(),
			"Try again when the host has free processes and memory.")
	case denied(err), errors.Is(err, unix.EINVAL) && !namespacesKnown(), errors.Is(err, unix.ENOSPC) && noUserNamespaces():
		return false, nil
	default:
		return false, failure("the check for user namespaces could not start: "+err.Error(),
			"Try again when the host has free processes and memory.")
	}
}

// denied says whether err is EPERM or EACCES: the kernel's clear no, from
// any step.
func denied(err error) bool {
	return errors.Is(err, unix.EPERM) || errors.Is(err, unix.EACCES)
}

// noUserNamespaces says whether the host allows no user namespace at all:
// max_user_namespaces is 0. Only then is clone's ENOSPC a clear no. Below a
// limit above 0, it means that other processes hold the namespaces for now.
func noUserNamespaces() bool {
	b, err := os.ReadFile(maxUserNS)
	return err == nil && strings.TrimSpace(string(b)) == "0"
}

// refusal says whether err, from the probe's build of a root, is the
// kernel's clear no: denied, or pivot_root's EINVAL on a rootfs root.
func refusal(err error) bool {
	return denied(err) || errors.Is(err, errRootfs)
}

// namespacesKnown says whether the kernel knows each namespace that a jail
// makes. Without one, clone fails with EINVAL, which is then a clear no.
func namespacesKnown() bool {
	for _, ns := range []string{"user", "mnt", "pid", "ipc", "uts", "cgroup", "net"} {
		if _, err := os.Lstat(nsDir + "/" + ns); err != nil {
			return false
		}
	}
	return true
}

// kernelABI returns the kernel's Landlock ABI, or 0 without Landlock.
func kernelABI() int {
	v, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION)
	if errno != 0 {
		return 0
	}
	return int(v)
}

// subuidFile and subgidFile list the users' subordinate IDs.
const (
	subuidFile = "/etc/subuid"
	subgidFile = "/etc/subgid"
)

// lookupSubID finds the login user's first subordinate user and group, and
// newuidmap and newgidmap. Without all four, a jail runs as the login user.
func lookupSubID() (subID, bool) {
	uid, gid := os.Getuid(), os.Getgid()
	name := ""
	if u, err := user.LookupId(strconv.Itoa(uid)); err == nil {
		name = u.Username
	}
	su, ok1 := rangeIn(subuidFile, name, uid)
	sg, ok2 := rangeIn(subgidFile, name, uid)
	nu, err1 := exec.LookPath("newuidmap")
	ng, err2 := exec.LookPath("newgidmap")
	if !ok1 || !ok2 || err1 != nil || err2 != nil {
		return subID{}, false
	}
	return subID{UID: uid, GID: gid, SubUID: su, SubGID: sg, NewUIDMap: nu, NewGIDMap: ng}, true
}

// Only the actual original read-only VMM source calls this admission. It
// retains the login UID and requires a real distinct configured second GID.
// SubUID -1 denotes this closed single-UID map; it is never a generic fallback.
func lookupReadonlyLoginMapV1() (subID, error) {
	uid, gid := os.Getuid(), os.Getgid()
	name := ""
	if u, err := user.LookupId(strconv.Itoa(uid)); err == nil {
		name = u.Username
	}
	sg, found := rangeIn(subgidFile, name, uid)
	ng, err := exec.LookPath("newgidmap")
	if !found || sg <= 0 || sg == gid || err != nil {
		return subID{}, vmmModeFailureV1("read-only login-UID execution requires a genuine distinct subordinate group and newgidmap")
	}
	return subID{UID: uid, GID: gid, SubUID: -1, SubGID: sg, NewGIDMap: ng}, nil
}

func rangeIn(file, name string, id int) (int, bool) {
	f, err := os.Open(file)
	if err != nil {
		return 0, false
	}
	defer func() { _ = f.Close() }()
	return firstRange(f, name, id)
}

// stageFailed writes why a stage failed, with what to try next, and
// returns the exit code of a failed set-up.
func stageFailed(err error) int {
	fmt.Fprintf(os.Stderr, "Error: %v\n", err)
	var pe *proto.Error
	if errors.As(err, &pe) && pe.Hint != "" {
		fmt.Fprintf(os.Stderr, "\nTry: %s\n", pe.Hint)
	}
	return setupFailed
}

// stepError is a jail error that keeps the system call's error, so that a
// caller can tell a refusal from a host that is short of memory.
type stepError struct {
	pe  *proto.Error
	err error
}

func (e *stepError) Error() string   { return e.pe.Message }
func (e *stepError) Unwrap() []error { return []error{e.pe, e.err} }

// stepFailed is failure, with the error of the step that failed.
func stepFailed(message, hint string, err error) error {
	return &stepError{pe: &proto.Error{Code: "jail", Message: message + ": " + err.Error(), Hint: hint}, err: err}
}
