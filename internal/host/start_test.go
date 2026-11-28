package host

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/paths"
)

func TestWaitStartedWaitsForTheHolder(t *testing.T) {
	alertHome(t)
	m := &Manifest{Schema: 1, ID: NewID(), Status: Status{State: StateStarting}}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(150 * time.Millisecond)
		mm := *m
		mm.Run.AgentPID, mm.Status.State = 4242, StateWorking
		_ = mm.Save()
	}()
	got := waitStarted(m.ID, 5*time.Second)
	if got == nil || got.Run.AgentPID != 4242 {
		t.Fatalf("waitStarted returned %+v before the holder wrote its start", got)
	}
	if got := waitStarted("nosuchsession", 50*time.Millisecond); got != nil {
		t.Fatalf("a missing session gave %+v", got)
	}
}

func TestStartFailedMessageNeverEmpty(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "supervisor.log")
	logBody := strings.Join([]string{
		`time=2026-11-28T00:00:00Z level=INFO msg="supervisor starting" session=abcd2345`,
		`time=2026-11-28T00:00:00Z level=ERROR msg="sandbox start failed" err="boom"`,
		`time=2026-11-28T00:00:00Z level=INFO msg="session socket open" peer_check=false`,
	}, "\n") + "\n"
	if err := os.WriteFile(logPath, []byte(logBody), 0o600); err != nil {
		t.Fatal(err)
	}

	code := 3
	if got := startFailedMessage(&Manifest{Status: Status{State: StateErrored, Reason: "exec format error"}}, logPath, 0); got != "exec format error" {
		t.Fatalf("explicit reason = %q, want exec format error", got)
	}
	if got := startFailedMessage(&Manifest{Status: Status{State: StateErrored}}, logPath, 0); !strings.Contains(got, "sandbox start failed: boom") {
		t.Fatalf("error log fallback = %q, want sandbox start failed: boom", got)
	}
	// When no ERROR line exists after from, fall back to exit code / signal / generic text.
	if got := startFailedMessage(&Manifest{Status: Status{State: StateErrored, ExitCode: &code}}, logPath, int64(len(logBody))); !strings.Contains(got, "exit code 3") {
		t.Fatalf("exit code fallback = %q, want exit code 3", got)
	}
	if got := startFailedMessage(&Manifest{Status: Status{State: StateErrored, Signal: "killed"}}, "", 0); !strings.Contains(got, "signal killed") {
		t.Fatalf("signal fallback = %q, want signal killed", got)
	}
	if got := startFailedMessage(&Manifest{Status: Status{State: StateErrored}}, "", 0); got == "" {
		t.Fatal("empty fallback returned empty string")
	}

	pe := startFailedError(errors.New(""))
	if pe.Code != "start_failed" || pe.Message == "" || pe.Hint == "" {
		t.Fatalf("startFailedError(\"\") = %+v, want non-empty message and hint", pe)
	}
}

func TestWaitSocketLogNeverReturnsEmptyErrorAndIgnoresStartedAgentExit(t *testing.T) {
	alertHome(t)
	id1 := NewID()
	m1 := &Manifest{Schema: 1, ID: id1, Status: Status{State: StateErrored}}
	if err := m1.Save(); err != nil {
		t.Fatal(err)
	}
	err := waitSocketLog(id1, paths.SupervisorLog(id1), 0, time.Second)
	if err == nil || strings.TrimSpace(err.Error()) == "" {
		t.Fatalf("waitSocketLog on empty-reason errored manifest = %v, want non-empty error", err)
	}

	// An agent that started (AgentPID != 0) and exited non-zero is not a start failure.
	id2 := NewID()
	code := 3
	m2 := &Manifest{
		Schema: 1,
		ID:     id2,
		Run:    Runtime{AgentPID: 4242},
		Status: Status{State: StateErrored, ExitCode: &code},
	}
	if err := m2.Save(); err != nil {
		t.Fatal(err)
	}
	sock := paths.Socket(id2)
	if err := paths.EnsurePrivateDir(filepath.Dir(sock)); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(80 * time.Millisecond)
		ln, lerr := ListenPrivate(sock, "test socket")
		if lerr == nil {
			t.Cleanup(func() {
				_ = ln.Close()
				_ = os.Remove(sock)
			})
		}
	}()
	if err := waitSocketLog(id2, paths.SupervisorLog(id2), 0, 2*time.Second); err != nil {
		t.Fatalf("waitSocketLog failed for started-then-exited agent: %v", err)
	}
}
