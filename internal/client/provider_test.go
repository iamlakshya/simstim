package client

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRunProvider(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the test scripts are POSIX shell; on Windows a provider is a .cmd or .exe")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "provider.sh")
	// The script echoes its action and the job it received on stdin.
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '{\"action\":\"%s\",\"job\":' \"$1\"; cat; printf '}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	h := Host{Name: "box", SSH: "box.example", Labels: map[string]string{"provider": "test"}, Provider: &Provider{Script: script}}
	out, err := RunProvider(context.Background(), h, "status")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Action string `json:"action"`
		Job    struct {
			Action string         `json:"action"`
			Host   map[string]any `json:"host"`
		} `json:"job"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("bad output %q: %v", out, err)
	}
	if got.Action != "status" || got.Job.Action != "status" || got.Job.Host["name"] != "box" {
		t.Fatalf("got %+v", got)
	}

	failing := filepath.Join(dir, "my fail.sh")
	_ = os.WriteFile(failing, []byte("#!/bin/sh\necho 'no credentials' >&2\nexit 3\n"), 0o755)
	h.Provider.Script = failing
	_, err = RunProvider(context.Background(), h, "start")
	if err == nil || !strings.Contains(err.Error(), "no credentials") {
		t.Fatalf("want the script's stderr in the error, got %v", err)
	}
	var ue *UserError
	if want := "Run '" + failing + "' start by hand to see what fails."; !errors.As(err, &ue) || ue.Hint != want {
		t.Errorf("hint = %v, want %q", err, want)
	}
}

// A hint that repeats an SSH target or a script's path quotes it, and keeps
// a leading dash from reading as an option.
func TestHintsQuoteTargetsAndPaths(t *testing.T) {
	for in, want := range map[string]string{
		"box": "ssh box", "dev@203.0.113.10": "ssh dev@203.0.113.10", "dev@x;y": "ssh 'dev@x;y'",
		"my host": "ssh 'my host'", "-oProxyCommand=sh": "ssh -- -oProxyCommand=sh", "-o x": "ssh -- '-o x'",
	} {
		if got := SSHCommand(in); got != want {
			t.Errorf("SSHCommand(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{"/opt/p.sh": "/opt/p.sh", "/my dir/p.sh": "'/my dir/p.sh'", "-p.sh": "./-p.sh", "a;b": "'a;b'"} {
		if got := pathWord(in); got != want {
			t.Errorf("pathWord(%q) = %q, want %q", in, got, want)
		}
	}
	s := &SSH{Host: Host{Name: "box", SSH: "dev@x;y"}}
	if got := s.ReachHint(); got != "ssh 'dev@x;y' (check that this works first)" {
		t.Errorf("ReachHint = %q", got)
	}
	err := sshError(Host{Name: "box", SSH: "-oProxyCommand=sh"}, exitErr255(t), "Permission denied")
	var ue *UserError
	if !errors.As(err, &ue) || ue.Hint != "ssh -- -oProxyCommand=sh (check that this works first)" {
		t.Errorf("sshError = %v", err)
	}
}

// exitErr255 is the error of a process that exited with 255, as ssh does
// when it cannot reach a host.
func exitErr255(t *testing.T) error {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the test runs sh")
	}
	err := exec.Command("sh", "-c", "exit 255").Run()
	if err == nil {
		t.Fatal("sh exited with 0")
	}
	return err
}
