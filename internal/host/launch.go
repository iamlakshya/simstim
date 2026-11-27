package host

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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

// startWait is how long a start waits for its holder's socket to answer.
var startWait = 5 * time.Second

// restoreWait is how much longer a restore waits for a holder that did not
// answer within startWait.
var restoreWait = 30 * time.Second

// unitEnv names the variable that tells a holder the unit that
// launchHolder started it in, so that the holder records the unit in its
// first save also where its cgroup does not show it, as with cgroup v1.
var unitEnv = brand.Env("UNIT")

// launchHolder starts the holder for m and waits until its socket answers.
// Without systemd, it returns the holder's process, which this process
// started: a start that fails stops that very process (stopStarting). The
// caller releases it.
func launchHolder(m *Manifest) (*os.Process, error) {
	if m.Files != nil {
		if err := seedConnectionAlive(); err != nil {
			return nil, err
		}
	}
	if err := ValidateManifestStart(m); err != nil {
		return nil, err
	}
	exe, err := holderExecutable()
	if err != nil {
		return nil, err
	}
	sub, stopSeconds, err := sessionCommand(m)
	if err != nil {
		return nil, err
	}
	// Every warning of the start goes to the session's log before the
	// holder opens it, so that the log has one writer while it runs. The
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
			// SIGTERM goes to the holder only, which stops its agent itself and
			// records why; anything left is killed after the timeout.
			"-p", "KillMode=mixed",
			"-p", "TimeoutStopSec=" + stopSeconds,
			"-p", "OOMPolicy=continue",
		}
		if m.Wall != nil {
			args = append(args, "-p", "TasksMax=4096")
		}
		if m.Limits.MemoryMax > 0 {
			args = append(args,
				"-p", fmt.Sprintf("MemoryMax=%d", m.Limits.MemoryMax),
				"-p", "MemorySwapMax=0")
		}
		// Close the managed holder's host-root inputs over this original
		// creating bridge, as the native supervised launch already does.
		for _, value := range managedOrdinaryEnvironmentV1(unit) {
			args = append(args, "--setenv="+value)
		}
		args = append(args, "-p", "UnsetEnvironment="+holderLockFDEnv, exe, sub, m.ID)
		from = logSize(m.ID)
		command := exec.CommandContext(context.Background(), "systemd-run", args...)
		var output bytes.Buffer
		command.Stdout, command.Stderr = &output, &output
		err := startSessionCommand(m, command)
		if err == nil {
			err = command.Wait()
		}
		out := output.Bytes()
		if err != nil {
			return nil, fmt.Errorf("systemd-run: %w: %s", err, strings.TrimSpace(string(out)))
		}
		m.Run.Unit = unit
		return nil, holderStarted(m.ID, from, startWait)
	}
	// No systemd user manager: start the holder in its own session, detached,
	// with holder.lock held across fork so ownership has no gap.
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("start the holder: %w", err)
	}
	defer devnull.Close()
	lockFile, err := openHolderLockForLaunch(m.ID)
	if err != nil {
		return nil, fmt.Errorf("start the holder: %w", err)
	}
	cmd := exec.CommandContext(context.Background(), exe, sub, m.ID)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = devnull, devnull, devnull
	cmd.ExtraFiles = []*os.File{lockFile}
	cmd.Env = append(os.Environ(), holderLockFDEnv+"=3")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	from = logSize(m.ID)
	if err := startSessionCommand(m, cmd); err != nil {
		lockFile.Close()
		return nil, fmt.Errorf("start the holder: %w", err)
	}
	lockFile.Close()
	m.Run.HolderPID = cmd.Process.Pid
	return cmd.Process, holderStarted(m.ID, from, startWait)
}

// holderStarted waits until the holder of session id answers. It writes
// nothing to the session's log, which the holder owns once it runs. When the
// start fails, it reports the last line that the holder wrote after the
// offset from, never a warning of the start itself.
func holderStarted(id string, from int64, timeout time.Duration) error {
	return waitSocket(id, from, timeout)
}

// logSize is the size of the session's log now, the offset from which the
// holder writes.
func logSize(id string) int64 {
	st, err := os.Stat(paths.HolderLog(id))
	if err != nil {
		return 0
	}
	return st.Size()
}

func waitSocket(id string, from int64, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		// The endpoint exists before manifest loading, so a raw connection
		// proves pending control, not readiness to receive secrets or run.
		if _, err := HolderRequest(id, "sockets", nil, min(200*time.Millisecond, time.Until(deadline))); err == nil {
			return nil
		}
		// The holder writes an errored status if the agent failed to start.
		if m, err := LoadManifest(id); err == nil && m.Status.State == StateErrored {
			return errors.New(m.Status.Reason)
		}
		time.Sleep(50 * time.Millisecond)
	}
	b, _ := os.ReadFile(paths.HolderLog(id))
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
