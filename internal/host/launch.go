package host

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/paths"
)

// SystemdUser reports whether a systemd user manager is running for this user.
func SystemdUser() bool {
	out, err := exec.CommandContext(context.Background(), "systemctl", "--user", "is-system-running").Output()
	s := strings.TrimSpace(string(out))
	if err != nil && s == "" {
		return false
	}
	return s == "running" || s == "degraded" || s == "starting"
}

// UnitName is the systemd unit of a session.
func UnitName(id string) string { return brand.UnitPrefix + id + ".service" }

// holderExecutable is the helper binary that holders are started from.
// Tests point it at a copy of the test binary with a shadow terminal next
// to it.
var holderExecutable = os.Executable

// launchHolder starts the holder for m and waits until its socket answers.
func launchHolder(m *Manifest) error {
	return launchSessionProcess(m, "holder", paths.HolderLog(m.ID), 15, 0, 5*time.Second)
}

// launchSupervisor starts the supervisor for a walled session m and waits
// until its session socket answers.
func launchSupervisor(m *Manifest) error {
	return launchSessionProcess(m, "supervise", paths.SupervisorLog(m.ID), 30, 4096, 10*time.Second)
}

// launchSessionProcess starts subcmd ("holder" or "supervise") for m in a
// systemd user unit (or detached when no user manager runs) and waits until
// paths.Socket(m.ID) answers.
func launchSessionProcess(m *Manifest, subcmd, logPath string, stopSec, tasksMax int, timeout time.Duration) error {
	exe, err := holderExecutable()
	if err != nil {
		return err
	}
	// Every warning of the start goes to the session's log before the
	// process opens it, so that the log has one writer while it runs. The
	// report of a failed start reads only what came after them.
	var from int64
	if SystemdUser() {
		// All sessions together stay below the host's memory minus the
		// reserve. Without that limit the session still starts, under its
		// own cap.
		if err := limitSessions(DefaultMemoryMax()); err != nil {
			warnSession(m.ID, "could not limit the memory of all sessions", "err", err)
		}
		unit := UnitName(m.ID)
		args := []string{"--user", "--quiet", "--collect",
			"--unit=" + unit, "--slice=" + brand.Slice,
			// SIGTERM goes to the main process only, which stops its children
			// itself and records why; anything left is killed after the timeout.
			"-p", "KillMode=mixed",
			"-p", fmt.Sprintf("TimeoutStopSec=%d", stopSec),
			"-p", "OOMPolicy=continue",
		}
		if tasksMax > 0 {
			args = append(args, "-p", fmt.Sprintf("TasksMax=%d", tasksMax))
		}
		if m.Limits.MemoryMax > 0 {
			args = append(args,
				"-p", fmt.Sprintf("MemoryMax=%d", m.Limits.MemoryMax),
				"-p", "MemorySwapMax=0")
		}
		if m.Limits.CPUs > 0 {
			args = append(args, "-p", fmt.Sprintf("CPUQuota=%d%%", m.Limits.CPUs*100))
		}
		args = append(args, exe, subcmd, m.ID)
		from = logFileSize(logPath)
		out, err := exec.CommandContext(context.Background(), "systemd-run", args...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("systemd-run: %w: %s", err, strings.TrimSpace(string(out)))
		}
		m.Run.Unit = unit
	} else {
		// No systemd user manager: start the process in its own session, detached.
		devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
		if err != nil {
			return fmt.Errorf("start the %s: %w", subcmd, err)
		}
		defer devnull.Close()
		cmd := exec.CommandContext(context.Background(), exe, subcmd, m.ID)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = devnull, devnull, devnull
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		from = logFileSize(logPath)
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("start the %s: %w", subcmd, err)
		}
		_ = cmd.Process.Release()
	}
	return waitSocketLog(m.ID, logPath, from, timeout)
}

// holderStarted waits until the holder of session id answers. It writes
// nothing to the session's log, which the holder owns once it runs. When the
// start fails, it reports the last line that the holder wrote after the
// offset from, never a warning of the start itself.
func holderStarted(id string, from int64, timeout time.Duration) error {
	return waitSocketLog(id, paths.HolderLog(id), from, timeout)
}

// logSize is the size of the session's holder log now.
func logSize(id string) int64 { return logFileSize(paths.HolderLog(id)) }

func logFileSize(path string) int64 {
	st, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return st.Size()
}

func waitSocketLog(id, logPath string, from int64, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		d := net.Dialer{Timeout: 200 * time.Millisecond}
		if c, err := d.DialContext(context.Background(), "unix", paths.Socket(id)); err == nil {
			c.Close()
			return nil
		}
		// The holder or supervisor writes an errored status if startup failed.
		if m, err := LoadManifest(id); err == nil && m.Status.State == StateErrored {
			return errors.New(m.Status.Reason)
		}
		time.Sleep(50 * time.Millisecond)
	}
	b, _ := os.ReadFile(logPath)
	if int64(len(b)) >= from { // a log that turned over since starts again at 0
		b = b[from:]
	}
	if len(strings.TrimSpace(string(b))) > 0 {
		lines := strings.Split(strings.TrimSpace(string(b)), "\n")
		return fmt.Errorf("the session did not start: %s", logText(lines[len(lines)-1]))
	}
	return errors.New("the session did not start in time")
}

// logText turns a line of a holder's log into text for the user: its message
// and its error, without the time and the level. A line in another form, such
// as one an older helper wrote, comes back as it is.
func logText(line string) string {
	var msg, errText string
	for rest := line; rest != ""; {
		key, val, ok := strings.Cut(rest, "=")
		if !ok {
			break
		}
		if strings.HasPrefix(val, `"`) {
			q, err := strconv.QuotedPrefix(val)
			if err != nil {
				break
			}
			rest = strings.TrimPrefix(val[len(q):], " ")
			val, _ = strconv.Unquote(q)
		} else {
			val, rest, _ = strings.Cut(val, " ")
		}
		switch key {
		case "msg":
			msg = val
		case "err":
			errText = val
		}
	}
	switch {
	case msg == "":
		return line
	case errText == "":
		return msg
	}
	return msg + ": " + errText
}
