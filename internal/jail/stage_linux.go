package jail

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// runStage runs one stage of a jail. It returns an exit code only when the
// command did not take this process's place.
func runStage(args []string) int {
	if len(args) == 2 && args[0] == stageGvisorOuterNativeV1 && args[1] == "3" {
		return nativeGvisorOuterStageV1()
	}
	if len(args) == 3 && (args[0] == stageGvisorMapV2 || args[0] == stageGvisorControllerMapV1) {
		count, err := strconv.Atoi(args[2])
		if err != nil || strconv.Itoa(count) != args[2] || count < 6 || count > maxGvisorSlotsV2 {
			return stageFailed(gvisorHandoffFailureV2("the original nested null vector count is invalid"))
		}
		if args[0] == stageGvisorControllerMapV1 {
			return stageFailed(runGvisorControllerMapV1(args[1], count))
		}
		return stageFailed(runGvisorMapV2(args[1], count))
	}
	if len(args) == 0 {
		return stageFailed(failure("this jail stage has no purpose", "Start jails with jail.Command, not by hand."))
	}
	if args[0] == stageProbe {
		return probe()
	}
	if len(args) < 2 {
		return stageFailed(failure("this jail stage has no set-up", "Start jails with jail.Command, not by hand."))
	}
	c, err := readSetup(args[1])
	if err != nil {
		return stageFailed(err)
	}
	stage, err := checkFileRulesStageV1(args[0], c)
	if err != nil {
		return stageFailed(err)
	}
	if err := checkStageFiles(c.Files); err != nil {
		return stageFailed(err)
	}
	if err := checkStageFileRulesV1(c); err != nil {
		return stageFailed(err)
	}
	switch stage {
	case stageOuter:
		return outer(c, os.Environ())
	case stageInit:
		return runInit(c, os.Environ())
	case stageExec:
		if c.ControllerPublicV1 != nil {
			return stageFailed(gvisorHandoffFailureV2("the fixed controller needs its original self-mapped ancestry entry"))
		}
		if err := enterVMMStageV1(c, stageExec); err != nil {
			return stageFailed(err)
		}
		return stageFailed(runExec(c, os.Environ()))
	case stageRemove:
		return removeInside(c)
	default:
		return stageFailed(failure("unknown jail stage "+quote(args[0]), "Report this as a bug."))
	}
}

// readSetup reads the stage's set-up from the descriptor that its command
// line names, then closes it.
func readSetup(arg string) (*setup, error) {
	fd, err := strconv.Atoi(arg)
	if err != nil || fd < 3 {
		return nil, failure("this jail stage's set-up descriptor "+quote(arg)+" is not valid",
			"Start jails with jail.Command, not by hand.")
	}
	f := os.NewFile(uintptr(fd), "setup")
	defer func() { _ = f.Close() }()
	var c setup
	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&c); err != nil || (len(c.Argv) == 0 && c.Remove == "") {
		return nil, failure("this jail stage's set-up cannot be read", "Start jails with jail.Command, not by hand.")
	}
	var more any
	if err := decoder.Decode(&more); !errors.Is(err, io.EOF) {
		return nil, failure("this jail stage's set-up has trailing content", "Start jails with jail.Command, not by hand.")
	}
	return &c, nil
}

// probe builds an empty root, as every jail does, in the namespaces that
// Userns tests, and exits as probeExit says.
func probe() int {
	runtime.LockOSThread()
	return probeExit(buildRoot(nil, nil, nil, nil, false))
}

// probeExit is the probe's exit code for the error of its build: 0, or
// probeRefused for a clear refusal, or probeUnclear.
func probeExit(err error) int {
	switch {
	case err == nil:
		return 0
	case refusal(err):
		return probeRefused
	default:
		return probeUnclear
	}
}

// outer starts init in new namespaces, maps its IDs to the subordinate
// ones with newuidmap and newgidmap, then waits for it. It passes on the
// signals that it gets, and exits as init exits. It is the leader of the
// jail's session and process group.
func outer(c *setup, env []string) int {
	// Never unlocked: init's parent-death signal follows this thread.
	runtime.LockOSThread()
	if err := cloexecFrom(3); err != nil {
		return stageFailed(err)
	}
	if err := enterVMMStageV1(c, stageOuter); err != nil {
		return stageFailed(err)
	}
	if stageHook != nil {
		go stageHook()
	}
	sigs := catchForwarded()
	syncR, syncW, err := os.Pipe()
	if err != nil {
		return stageFailed(failure("cannot make the jail's pipe: "+err.Error(), "Check the host's open-file limit."))
	}
	// syncW stays open until this stage returns: init reads its end to
	// learn that this stage still runs.
	defer func() { _ = syncW.Close() }()
	in := *c
	extra := inherited(c, true)
	// Private purpose slots precede the mapping synchronisation pipe.
	// Derive the actual next child slot rather than colliding with its lease.
	in.SyncFD = 3 + len(extra)
	cmd := exec.CommandContext(context.Background(), self)
	cmd.Args = []string{argv0, fileRulesStageV1(stageInit, c), strconv.Itoa(3 + c.Files)}
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	attr := namespaces(c.Net, false, initCaps)
	attr.Pdeathsig = unix.SIGKILL
	createdFD := -1
	if c.VMMExecV1 != nil {
		attr.PidFD = &createdFD
	}
	cmd.SysProcAttr = attr
	setupW, err := startWithSync(cmd, extra, 3+c.Files, syncR)
	if err != nil {
		return stageFailed(err)
	}
	if err := reportVMMCreationV1(c, stageOuter, cmd.Process, createdFD); err != nil {
		_ = setupW.Close()
		_ = cmd.Process.Kill()
		waitErr := cmd.Wait()
		closeErr := closeInitInheritedV1(extra)
		return stageFailed(errors.Join(err, waitErr, closeErr))
	}
	if err := writeChildSetup(cmd, setupW, &in); err != nil {
		return stageFailed(err)
	}
	if err := mapIDs(context.Background(), c.Sub, cmd.Process.Pid); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return stageFailed(err)
	}
	if _, err := syncW.Write([]byte{1}); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return stageFailed(failure("the jail's init ended during set-up", "Read the error above."))
	}
	if err := finishVMMCreatorV1(c, stageOuter, extra); err != nil {
		_ = cmd.Process.Kill()
		waitErr := cmd.Wait()
		return stageFailed(errors.Join(err, waitErr))
	}
	relay := startRelay(cmd.Process.Pid, sigs, unix.Kill)
	waitErr := waitChildWithRelay(cmd.Process.Pid, relay, cmd.Wait, cmd.Process.Kill)
	if waitErr != nil {
		return stageFailed(stepFailed("cannot wait for the jail's init", "Read the jail's stderr, then try again.", waitErr))
	}
	runtime.KeepAlive(syncW)
	return exitCode(cmd.ProcessState)
}

// startWithSync drops the outer stage's read end after the child has
// inherited it, including when starting the child failed.
func startWithSync(cmd *exec.Cmd, extra []*os.File, at int, syncR *os.File) (*os.File, error) {
	w, err := startWith(cmd, append(extra, syncR), at)
	_ = syncR.Close()
	return w, err
}

// inherited returns the descriptors that a stage passes to the next, in
// their places from fd 3: the files, a place for the set-up, the status
// (or nothing), then the pinned binds and rules.
func inherited(c *setup, status bool) []*os.File {
	fds := make([]*os.File, 0, c.Files+2+len(c.Binds)+len(c.Rules))
	for i := range c.Files {
		fds = append(fds, os.NewFile(uintptr(3+i), "inherited"))
	}
	var st *os.File
	if status {
		st = os.NewFile(uintptr(c.StatusFD), "status")
	}
	fds = append(fds, nil, st)
	for _, b := range c.Binds {
		fds = append(fds, os.NewFile(uintptr(b.FD), b.Path))
	}
	for _, r := range pinnedRules(c.Rules) {
		fds = append(fds, os.NewFile(uintptr(r.FD), r.Path))
	}
	return appendPublicationInheritedV1(c, appendVMMInheritedV1(c, fds))
}

// pinnedRules are the rules that have their own descriptor.
func pinnedRules(rules []rule) []rule {
	var out []rule
	for _, r := range rules {
		if r.FD > 0 {
			out = append(out, r)
		}
	}
	return out
}

// startWith starts cmd with extra from fd 3, and a new set-up pipe at fd
// at, which takes the place that extra leaves for it. It returns the pipe's
// write end.
func startWith(cmd *exec.Cmd, extra []*os.File, at int) (*os.File, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, failure("cannot make the jail's pipe: "+err.Error(), "Check the host's open-file limit.")
	}
	extra[at-3] = r
	cmd.ExtraFiles = extra
	err = cmd.Start()
	_ = r.Close()
	if err != nil {
		_ = w.Close()
		return nil, failure("cannot start the jail's next stage: "+err.Error(),
			"Check that this host gives user namespaces, or run jails without a subuid.")
	}
	return w, nil
}

func writeSetup(w *os.File, c *setup) error {
	defer func() { _ = w.Close() }()
	if err := json.NewEncoder(w).Encode(c); err != nil {
		return failure("cannot pass the jail's set-up on: "+err.Error(), "Read the error above.")
	}
	return nil
}

// forwarded are the signals that the jail passes to its command.
var forwarded = []os.Signal{unix.SIGTERM, unix.SIGINT, unix.SIGHUP, unix.SIGQUIT,
	unix.SIGUSR1, unix.SIGUSR2, unix.SIGWINCH}

// mapIDs maps the login user and group to 0 in pid's user namespace, and
// the first subordinate user and group to 1.
func mapIDs(ctx context.Context, s *subID, pid int) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	p := strconv.Itoa(pid)
	for _, m := range []struct {
		prog     string
		id, sub  int
		filename string
	}{{s.NewUIDMap, s.UID, s.SubUID, subuidFile}, {s.NewGIDMap, s.GID, s.SubGID, subgidFile}} {
		out, err := exec.CommandContext(ctx, m.prog, p, "0", strconv.Itoa(m.id), "1", "1", strconv.Itoa(m.sub), "1").CombinedOutput()
		if err != nil {
			return failure("cannot map the jail's IDs with "+m.prog+": "+strings.TrimSpace(string(out)+" "+err.Error()),
				"Check this user's range in "+m.filename+", or run jails without a subuid.")
		}
	}
	return nil
}

// exitCode is a stage's exit code for a child that ended as st says.
func exitCode(st *os.ProcessState) int {
	if ws, ok := st.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return st.ExitCode()
}

// awaitMaps waits until the outer stage has mapped this process's IDs.
func awaitMaps(fd int) error {
	b := make([]byte, 1)
	if n, err := unix.Read(fd, b); err != nil || n != 1 {
		return failure("the jail's outer stage could not map its user and group IDs",
			"Check that newuidmap and newgidmap work for this user, or run jails without a subuid.")
	}
	return nil
}

// RemoveAll removes path and everything in it, as os.RemoveAll does. A
// subuid jail owns what it made in its writable binds, so where the login
// user cannot remove a file, RemoveAll tries again in a user namespace with
// the jail's mapping, where the login user is root over both IDs.
func RemoveAll(ctx context.Context, path string) error {
	err := os.RemoveAll(path)
	if err == nil || !errors.Is(err, fs.ErrPermission) {
		return removeFailed(path, err)
	}
	sub, ok := lookupSubID()
	if !ok || !initDone.Load() || !Userns() {
		return removeFailed(path, err)
	}
	cmd := exec.CommandContext(ctx, self)
	cmd.Args = []string{argv0, stageRemove, "3"}
	cmd.Env = []string{}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWUSER, Pdeathsig: unix.SIGKILL,
		AmbientCaps: []uintptr{unix.CAP_DAC_OVERRIDE, unix.CAP_DAC_READ_SEARCH, unix.CAP_FOWNER}}
	syncR, syncW, perr := os.Pipe()
	if perr != nil {
		return removeFailed(path, perr)
	}
	defer func() { _ = syncW.Close() }()
	setupW, serr := startWith(cmd, []*os.File{nil, syncR}, 3)
	_ = syncR.Close()
	if serr != nil {
		return serr
	}
	if err := writeChildSetup(cmd, setupW, &setup{Remove: path, SyncFD: 4}); err != nil {
		return err
	}
	if err := mapIDs(ctx, &sub, cmd.Process.Pid); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return err
	}
	_, _ = syncW.Write([]byte{1})
	if err := cmd.Wait(); err != nil {
		return removeFailed(path, errors.New(strings.TrimSpace(stderr.String())+" "+err.Error()))
	}
	return nil
}

// writeChildSetup reaps a started removal stage when it cannot receive its
// set-up. Wait also joins the stderr copier that exec.Cmd may have made.
func writeChildSetup(cmd *exec.Cmd, writer *os.File, config *setup) error {
	if err := writeSetup(writer, config); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return err
	}
	return nil
}

func removeFailed(path string, err error) error {
	if err == nil {
		return nil
	}
	return failure("cannot remove "+quote(path)+": "+err.Error(),
		"Check that the folder belongs to this user, or to its subuid in /etc/subuid.")
}

// removeInside removes the set-up's folder, once outer's maps make the
// login user root here over the login user's files and the subuid's.
func removeInside(c *setup) int {
	if err := awaitMaps(c.SyncFD); err != nil {
		return stageFailed(err)
	}
	if err := os.RemoveAll(c.Remove); err != nil { //nolint:gosec // G703: the path comes from RemoveAll, in this program
		_, _ = io.WriteString(os.Stderr, err.Error())
		return 1
	}
	return 0
}
