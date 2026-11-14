package jail

import (
	"errors"
	"io"
	"os"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// runInit builds the jail, confines itself as the command will be, and
// starts the exec stage, which becomes the command. It then stays as the
// jail's init: it passes signals on, reaps orphans, and when the command
// ends, kills every process of the jail that is left. With namespaces it
// is PID 1 of the jail's PID namespace. Without them, it is a child
// subreaper, and the leader of a process group that no process of the jail
// can leave. It writes how the command ended to its status pipe, and exits
// with the command's code.
func runInit(c *setup, env []string) int {
	// Never unlocked: the confinement below changes this thread, and the
	// exec stage starts from it.
	runtime.LockOSThread()
	if err := cloexecFrom(3); err != nil {
		return stageFailed(err)
	}
	var status *os.File
	if c.StatusFD > 0 {
		status = os.NewFile(uintptr(c.StatusFD), "status")
	}
	sigs := catchForwarded()
	pid, err := startCommand(c, env)
	if err != nil {
		return stageFailed(err)
	}
	relay := startRelay(pid, sigs, unix.Kill)
	ws, err := reapWithRelay(pid, relay)
	if afterMainReap != nil {
		afterMainReap()
	}
	endRest()
	if err != nil {
		return stageFailed(err)
	}
	if status != nil {
		_, _ = io.WriteString(status, exitStatus(ws))
		_ = status.Close()
	}
	if ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return ws.ExitStatus()
}

// afterMainReap is a test gate before orphan cleanup and status output.
var afterMainReap func()

// startCommand builds the root, confines this stage, and starts the exec
// stage. It returns the command's process ID.
func startCommand(c *setup, env []string) (int, error) {
	helper, err := unix.Open(self, unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return 0, stepFailed("cannot open this program", "Report this as a bug.", err)
	}
	defer func() { _ = unix.Close(helper) }()
	if c.SyncFD > 0 {
		if err := awaitMaps(c.SyncFD); err != nil {
			return 0, err
		}
	}
	if !c.Userns && kernelABI() < minABI {
		return 0, failure("a jail without a user namespace needs Landlock ABI 3 (Linux 6.2) or later, and this kernel has ABI "+
			strconv.Itoa(kernelABI()), "Update the host's kernel to Linux 6.2 or later.")
	}
	fds, err := reopen(c.Binds)
	if err != nil {
		return 0, err
	}
	err = verifyRules(c, fds)
	if err == nil && c.Userns {
		var execs []execMount
		var execFDs []int
		execs, execFDs, err = execMounts(c, fds)
		cwd, _ := os.Getwd()
		if err == nil {
			err = buildRoot(c.Binds, fds, execs, execFDs, c.Sub != nil)
		}
		// buildRoot leaves the process in "/", if cwd is gone inside the jail.
		if err == nil && cwd != "" {
			_ = unix.Chdir(cwd)
		}
		fds = append(fds, execFDs...)
	}
	for _, fd := range fds {
		_ = unix.Close(fd)
	}
	if err != nil {
		return 0, err
	}
	rs, err := initRules(c, helper)
	if err != nil {
		return 0, err
	}
	progs, err := filters(c.Seccomp, c.Net, !c.Userns)
	if err != nil {
		return 0, err
	}
	if !c.Userns {
		if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
			return 0, stepFailed("cannot make the jail's init a subreaper", "Update the host's kernel: it needs Linux 3.4.", err)
		}
	}
	if err := lockDown(c, rs, progs); err != nil {
		return 0, err
	}
	return startExec(c, env)
}

// initRules are init's own Landlock rules: those of the command, the whole
// of /proc for reading (the exec stage narrows it), and this program, which
// the exec stage runs.
func initRules(c *setup, helper int) (*ruleset, error) {
	rs, err := commandRules(c)
	if err != nil {
		return nil, err
	}
	proc := Read
	for _, r := range c.Rules {
		if r.FD == 0 {
			proc |= r.Access
		}
	}
	if err := rs.addPath("/proc", proc); err != nil {
		return nil, err
	}
	if err := rs.add(helper, Read|Exec); err != nil {
		return nil, err
	}
	return rs, nil
}

// commandRules makes a ruleset with the rules of the binds, of the pinned
// rules, and of the inherited file that the command runs from.
func commandRules(c *setup) (*ruleset, error) {
	if err := checkStageFileRulesV1(c); err != nil {
		return nil, err
	}
	rs, err := newRuleset(kernelABI(), c.Net)
	if err != nil {
		return nil, err
	}
	for _, b := range c.Binds {
		a := b.access()
		if err := rs.add(b.FD, a); err != nil {
			return nil, err
		}
	}
	for _, r := range pinnedRules(c.Rules) {
		if err := rs.add(r.FD, r.Access); err != nil {
			return nil, err
		}
	}
	for _, rule := range c.FileRulesV1 {
		if err := rs.add(rule.FD, rule.Access); err != nil {
			return nil, err
		}
	}
	if c.ExecFD > 0 {
		if err := rs.add(c.ExecFD, Read|Exec); err != nil {
			return nil, err
		}
	}
	return rs, nil
}

// startExec starts the exec stage from this thread, so that it inherits
// this stage's confinement. It dies with this stage.
func startExec(c *setup, env []string) (int, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return 0, stepFailed("cannot make the jail's pipe", "Check the host's open-file limit.", err)
	}
	extra := inherited(c, false)
	extra[c.Files] = r
	files := append([]*os.File{os.Stdin, os.Stdout, os.Stderr}, extra...)
	//nolint:gosec // G702: this program starts itself again
	p, err := os.StartProcess(self, []string{argv0, fileRulesStageV1(stageExec, c), strconv.Itoa(3 + c.Files)},
		&os.ProcAttr{Env: env, Files: files, Sys: &syscall.SysProcAttr{Pdeathsig: unix.SIGKILL}})
	_ = r.Close()
	if err != nil {
		_ = w.Close()
		return 0, stepFailed("cannot start the jail's command", "Read the error above.", err)
	}
	next := *c
	next.StatusFD, next.SyncFD, next.Sub = 0, 0, nil
	if err := writeSetup(w, &next); err != nil {
		return 0, err
	}
	return p.Pid, nil
}

// reap reaps every child until the command ends, and returns how it ended.
// The other children are orphans of the jail.
func reap(pid int) syscall.WaitStatus {
	for {
		var ws syscall.WaitStatus
		got, err := syscall.Wait4(-1, &ws, 0, nil)
		switch {
		case errors.Is(err, syscall.EINTR):
		case err != nil || got == pid:
			return ws
		}
	}
}

// endRest kills and reaps every child that is left, again and again, as
// their own children come to this subreaper.
func endRest() {
	for {
		kids := restChildren()
		for _, k := range kids {
			_ = unix.Kill(k, unix.SIGKILL)
		}
		for {
			var ws syscall.WaitStatus
			got, err := syscall.Wait4(-1, &ws, unix.WNOHANG|unix.WALL, nil)
			if errors.Is(err, syscall.ECHILD) {
				return
			}
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			if got <= 0 || err != nil {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
}
