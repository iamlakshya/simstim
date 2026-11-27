//go:build linux

package classify

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const commandAnswer = `{"action":"allow","reason":"documentation","scope":"session"}`

func commandScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "judge")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nset -eu\n"+body+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func quotedPath(path string) string { return "'" + strings.ReplaceAll(path, "'", "'\"'\"'") + "'" }

func TestCommandJudgeRunsExactExecutableWithBoundedInputAndNoSessionEnvironment(t *testing.T) {
	capture := filepath.Join(t.TempDir(), "request.json")
	t.Setenv("JUDGE_SESSION_SECRET", "must-not-enter-command")
	program := commandScript(t, "test -z \"${JUDGE_SESSION_SECRET:-}\"\ncat > "+quotedPath(capture)+"\nprintf '%s\\n' '"+commandAnswer+"'")
	judge, err := NewCommand(program, "once")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	result, err := judge.Decide(ctx, testInput())
	if err != nil || result.Action != "allow" || result.Scope != "once" {
		t.Fatalf("command answer failed: %+v %v", result, err)
	}
	raw, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	var actual Input
	if json.Unmarshal(raw, &actual) != nil || actual.Host != testInput().Host || strings.Contains(string(raw), "must-not-enter-command") {
		t.Fatal("command input was not the sole structured request")
	}
}

func TestCommandJudgeCancellationKillsAndJoinsItsProcessGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	program := commandScript(t, "sleep 60 &\nprintf '%s' \"$!\" > "+quotedPath(pidFile)+"\nwait")
	judge, err := NewCommand(program, "session")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { _, err := judge.Decide(ctx, testInput()); done <- err }()
	pid := awaitCommandChild(t, pidFile)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled judge allowed")
		}
	case <-time.After(time.Second):
		t.Fatal("command was not physically joined")
	}
	assertCommandChildStopped(t, pid)
}

func TestCommandJudgeSuccessfulParentStillCleansRemainingGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	program := commandScript(t, "sleep 60 > /dev/null 2>&1 &\nprintf '%s' \"$!\" > "+quotedPath(pidFile)+"\ncat > /dev/null\nprintf '%s\\n' '"+commandAnswer+"'")
	judge, err := NewCommand(program, "session")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, err := judge.Decide(ctx, testInput()); err != nil {
		t.Fatal(err)
	}
	assertCommandChildStopped(t, awaitCommandChild(t, pidFile))
}

func TestCommandJudgeRejectsFailureAndUnboundedOutput(t *testing.T) {
	for _, body := range []string{"cat > /dev/null\nprintf '%s' '" + commandAnswer + "'\nexit 1", "head -c 5000 /dev/zero", "printf '%s' 'not JSON'"} {
		judge, err := NewCommand(commandScript(t, body), "session")
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		_, err = judge.Decide(ctx, testInput())
		cancel()
		if err == nil {
			t.Fatal("failed or oversized judge allowed")
		}
	}
}

func awaitCommandChild(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(path)
		if err == nil {
			if pid, err := strconv.Atoi(string(raw)); err == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("judge child was not started")
	return 0
}

func assertCommandChildStopped(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) == syscall.ESRCH {
			return
		}
		raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
		if os.IsNotExist(err) || strings.Contains(string(raw), ") Z ") {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("judge child survived owned group cleanup")
}
