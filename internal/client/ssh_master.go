package client

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"
)

var masterExitTimeout = 5 * time.Second

// SetMasterExitTimeout changes how long RetireMaster waits on ssh -O exit and
// returns a function that restores the previous timeout.
func SetMasterExitTimeout(d time.Duration) func() {
	old := masterExitTimeout
	masterExitTimeout = d
	return func() { masterExitTimeout = old }
}

// RetireMaster closes the shared SSH ControlMaster for this host and checks
// that none remains running. When no shared connection is open, it returns nil.
// When an existing master refuses to exit or times out, it returns a hinted
// UserError so callers do not silently open a new channel over the old master.
func (s *SSH) RetireMaster(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !controlMaster || s.Host.SSH == "" {
		return nil
	}
	args, err := s.connArgs(ctx, true)
	if err != nil {
		return err
	}
	if !hasControlMasterArg(args) {
		return nil
	}
	exitArgs := append(append([]string(nil), args...), "-O", "exit", "--", s.Host.SSH)
	ectx, cancel := context.WithTimeout(ctx, masterExitTimeout)
	defer cancel()
	cmd := masterCommand(ectx, exitArgs)
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(ectx.Err(), context.DeadlineExceeded) {
		return s.retireError(args, "timed out")
	}
	if err != nil {
		msg := oneLineDetail(string(out))
		if noMasterSocket(msg) {
			return nil
		}
		if msg == "" {
			msg = err.Error()
		}
		return s.retireError(args, msg)
	}
	return s.waitMasterStopped(ctx, args)
}

func hasControlMasterArg(args []string) bool {
	for _, a := range args {
		if a == "ControlMaster=auto" {
			return true
		}
	}
	return false
}

func noMasterSocket(msg string) bool {
	return strings.Contains(msg, "No such file or directory") || strings.Contains(msg, "Connection refused")
}

func oneLineDetail(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) == 0 {
		return ""
	}
	return strings.TrimSpace(lines[len(lines)-1])
}

func (s *SSH) waitMasterStopped(ctx context.Context, baseArgs []string) error {
	checkArgs := append(append([]string(nil), baseArgs...), "-O", "check", "--", s.Host.SSH)
	deadline := time.Now().Add(min(500*time.Millisecond, masterExitTimeout))
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		cctx, cancel := context.WithTimeout(ctx, masterExitTimeout)
		cmd := masterCommand(cctx, checkArgs)
		out, err := cmd.CombinedOutput()
		timedOut := errors.Is(cctx.Err(), context.DeadlineExceeded)
		cancel()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if timedOut {
			return s.retireError(baseArgs, "timed out")
		}
		if err != nil {
			msg := oneLineDetail(string(out))
			if noMasterSocket(msg) {
				return nil
			}
			if msg == "" {
				msg = err.Error()
			}
			return s.retireError(baseArgs, msg)
		}
		running, _ := parseMaster(string(out))
		if !running {
			return nil
		}
		if time.Now().After(deadline) {
			return s.retireError(baseArgs, "the shared connection is still running")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func masterCommand(ctx context.Context, args []string) *exec.Cmd {
	args = append([]string{"-o", "LogLevel=ERROR"}, args...)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	cmd.WaitDelay = 500 * time.Millisecond
	return cmd
}

func (s *SSH) retireError(args []string, detail string) error {
	msg := "could not close the shared SSH connection to " + s.Host.Name
	if detail != "" {
		msg += ": " + detail
	}
	words := []string{"ssh"}
	for _, arg := range append(append([]string(nil), args...), "-O", "exit", "--", s.Host.SSH) {
		words = append(words, ShellWord(arg))
	}
	return &UserError{
		Msg:  msg,
		Hint: strings.Join(words, " "),
		Code: "reconnect_failed",
	}
}
