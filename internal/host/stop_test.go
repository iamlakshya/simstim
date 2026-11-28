package host

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/paths"
)

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestStopHookVerificationAndExecution(t *testing.T) {
	alertHome(t)
	dir := t.TempDir()
	out := filepath.Join(dir, "hook.out")
	repoStop := filepath.Join(dir, "."+brand.Name, "stop")
	writeExecutable(t, repoStop, "#!/bin/sh\necho \"hook=$"+brand.Env("HOOK")+" id=$"+brand.Env("SESSION")+" name=$"+brand.Env("SESSION_NAME")+" dir=$"+brand.Env("SESSION_DIR")+"\" > "+shellQuote(out)+"\n")

	s, err := setupHash(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.stop != repoStop {
		t.Fatalf("setupHash stop = %q, want %q", s.stop, repoStop)
	}

	m := &Manifest{
		ID:        "stophk01",
		Name:      "app",
		Cwd:       dir,
		Env:       os.Environ(),
		StopHook:  repoStop,
		SetupHash: s.hash,
	}
	if err := paths.EnsurePrivateDir(paths.SessionDir(m.ID)); err != nil {
		t.Fatal(err)
	}
	runStopHook(m)
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("trusted stop hook did not run: %v", err)
	}
	want := "hook=stop id=stophk01 name=app dir=" + dir
	if strings.TrimSpace(string(got)) != want {
		t.Fatalf("stop hook wrote %q, want %q", strings.TrimSpace(string(got)), want)
	}

	// Tamper with the repository stop script: stopHook must skip it, log a
	// warning, and fall back to the host's own stop hook if present.
	_ = os.Remove(out)
	writeExecutable(t, repoStop, "#!/bin/sh\necho tampered > "+shellQuote(out)+"\n")
	hostOut := filepath.Join(dir, "host-stop.out")
	writeExecutable(t, hostHookPath("stop"), "#!/bin/sh\necho host-stop > "+shellQuote(hostOut)+"\n")

	runStopHook(m)
	if _, err := os.Stat(out); err == nil {
		t.Fatal("tampered repository stop hook ran")
	}
	b, err := os.ReadFile(hostOut)
	if err != nil || strings.TrimSpace(string(b)) != "host-stop" {
		t.Fatalf("host fallback stop hook did not run: %q %v", string(b), err)
	}
	logBytes, err := os.ReadFile(paths.HolderLog(m.ID))
	if err != nil || !strings.Contains(string(logBytes), "skipped the stop script") {
		t.Fatalf("holder log missing skip warning: %v\n%s", err, logBytes)
	}
}

func TestStopHookTimeoutKillsProcessGroup(t *testing.T) {
	alertHome(t)
	old := stopHookTimeout
	stopHookTimeout = 150 * time.Millisecond
	t.Cleanup(func() { stopHookTimeout = old })

	dir := t.TempDir()
	hook := filepath.Join(dir, "slow-stop")
	writeExecutable(t, hook, "#!/bin/sh\nsleep 30 &\nsleep 30\n")

	m := &Manifest{
		ID:       "slowhk01",
		Name:     "slow",
		Cwd:      dir,
		Env:      os.Environ(),
		StopHook: hook,
	}
	if err := paths.EnsurePrivateDir(paths.SessionDir(m.ID)); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	runStopHook(m)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("runStopHook took %v, want timeout near 150ms", elapsed)
	}
	logBytes, err := os.ReadFile(paths.HolderLog(m.ID))
	if err != nil || !strings.Contains(string(logBytes), "stop hook timed out") {
		t.Fatalf("holder log missing timeout warning: %v\n%s", err, logBytes)
	}
}
