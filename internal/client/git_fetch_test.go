//go:build !windows

package client

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func newFetchFixture(t *testing.T) (string, Host) {
	t.Helper()
	dir := fakeSSH(t)
	h := Host{Name: "box", SSH: "box.example"}
	pinFor(t, h.Name)
	repo := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	t.Chdir(repo)
	return dir, h
}

func TestGitFetchUsesActualSSHExitAndPrivateLog(t *testing.T) {
	for _, kind := range []string{"destination", "hop", "signal", "remote"} {
		t.Run(kind, func(t *testing.T) {
			dir, h := newFetchFixture(t)
			stop := "Host key for " + pinAlias(h.Name) + " has changed and you have requested strict checking.\nHost key verification failed.\n"
			switch kind {
			case "destination":
				put(t, dir, "stderr", stop)
			case "hop":
				put(t, dir, "hop-stderr", stop)
			case "signal":
				put(t, dir, "stderr", stop)
				put(t, dir, "kill-self", "")
			case "remote":
				put(t, dir, "stderr", stop)
				put(t, dir, "exit-code", "1")
			}
			_, err := RunGitFetch(context.Background(), h, "/repo", "main", "main", false)
			if err == nil {
				t.Fatal("failed fake SSH returned success")
			}
			if IsHostKeyError(err) != (kind == "destination") {
				t.Fatalf("%s: %v", kind, err)
			}
			if kind == "destination" {
				var e *UserError
				if !errors.As(err, &e) || e.Kind != KindRefused {
					t.Fatalf("lost typed refusal: %v", err)
				}
			}
		})
	}
}

func TestGitFetchPassesProtocolInputAndGetsTheBranch(t *testing.T) {
	dir, h := newFetchFixture(t)
	remote := filepath.Join(t.TempDir(), "space 'quote'")
	for _, args := range [][]string{{"init", "-q", remote}, {"-C", remote, "config", "user.name", "Fixture"}, {"-C", remote, "config", "user.email", "fixture.invalid"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %v %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(remote, "file"), []byte("the branch\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"-C", remote, "add", "file"}, {"-C", remote, "commit", "-qm", "Fixture"}, {"-C", remote, "branch", "-M", "main"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %v %s", args, err, out)
		}
	}
	path := filepath.Join(dir, "ssh")
	script, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	needle := "[ -f \"$FAKE_SSH/hang\" ] && exec sleep 10"
	serve := "if [ -f \"$FAKE_SSH/serve\" ]; then for argument; do remote=$argument; done; exec sh -c \"$remote\"; fi\n" + needle
	if !strings.Contains(string(script), needle) {
		t.Fatal("fake SSH shape changed")
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(script), needle, serve, 1)), 0o700); err != nil {
		t.Fatal(err)
	}
	put(t, dir, "serve", "")
	if out, err := RunGitFetch(context.Background(), h, remote, "main", "review", false); err != nil {
		t.Fatalf("fetch: %v %s", err, out)
	}
	want, err := exec.Command("git", "-C", remote, "rev-parse", "main").Output()
	if err != nil {
		t.Fatal(err)
	}
	got, err := exec.Command("git", "rev-parse", "review").Output()
	if err != nil || string(got) != string(want) {
		t.Fatalf("fetched %q, want %q: %v", got, want, err)
	}
}

func TestGitFetchCancellationEndsItsSSH(t *testing.T) {
	dir, h := newFetchFixture(t)
	path := filepath.Join(dir, "ssh")
	script, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	needle := "[ -f \"$FAKE_SSH/hang\" ] && exec sleep 10"
	replacement := "if [ -f \"$FAKE_SSH/hang\" ]; then echo $$ > \"$FAKE_SSH/child\"; exec sleep 10; fi"
	if err := os.WriteFile(path, []byte(strings.Replace(string(script), needle, replacement, 1)), 0o700); err != nil {
		t.Fatal(err)
	}
	put(t, dir, "hang", "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := RunGitFetch(ctx, h, "/repo", "main", "review", false); done <- err }()
	var pid int
	deadline := time.After(5 * time.Second)
	for pid == 0 {
		if b, err := os.ReadFile(filepath.Join(dir, "child")); err == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		}
		if pid != 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("SSH did not start")
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("fetch = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("fetch ignored cancellation")
	}
	if out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "pid=").Output(); err == nil || strings.TrimSpace(string(out)) != "" {
		t.Fatalf("owned SSH still runs: %s, %v", out, err)
	}
}

func TestGitFetchDoesNotFollowRepositoryRemoteOverrides(t *testing.T) {
	for _, kind := range []string{"endpoint", "repository", "port", "local-program", "local-helper", "uploadpack"} {
		t.Run(kind, func(t *testing.T) {
			dir, h := newFetchFixture(t)
			remote := h.SSH + ":/repo"
			set := func(key, value string) {
				t.Helper()
				if out, err := exec.Command("git", "config", key, value).CombinedOutput(); err != nil {
					t.Fatalf("git config: %v %s", err, out)
				}
			}
			marker := filepath.Join(t.TempDir(), "executed")
			switch kind {
			case "endpoint":
				set("remote."+remote+".url", "other.example:/repo")
			case "repository":
				set("remote."+remote+".url", h.SSH+":/elsewhere")
			case "port":
				set("remote."+remote+".url", "ssh://"+h.SSH+":2222/repo")
			case "uploadpack":
				set("remote."+remote+".url", remote)
				set("remote."+remote+".uploadpack", "git-upload-pack /repo; printf unexpected")
			case "local-helper":
				program := filepath.Join(dir, "git-remote-fixture")
				if err := os.WriteFile(program, []byte("#!/bin/sh\nprintf EXECUTED > "+shellWord(marker)+"\nexit 1\n"), 0o700); err != nil {
					t.Fatal(err)
				}
				set("remote."+remote+".url", remote)
				set("remote."+remote+".vcs", "fixture")
			case "local-program":
				script := filepath.Join(t.TempDir(), "program")
				if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf EXECUTED > "+shellWord(marker)+"\nexit 1\n"), 0o700); err != nil {
					t.Fatal(err)
				}
				set("remote."+remote+".url", "ext::sh "+script)
				set("protocol.ext.allow", "always")
			}
			if _, err := RunGitFetch(context.Background(), h, "/repo", "main", "review", false); err == nil {
				t.Fatal("fake SSH fetch returned success")
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("repository config ran a local program: %v", err)
			}
			observed := false
			for _, call := range sshCalls(t, dir) {
				if !strings.Contains(call, "git-upload-pack") {
					continue
				}
				observed = true
				if kind != "uploadpack" {
					t.Fatalf("repository config redirected SSH: %s", call)
				}
				if strings.Contains(call, "unexpected") || !strings.Contains(call, "git-upload-pack '/repo'") {
					t.Fatalf("repository config replaced upload-pack: %s", call)
				}
			}
			if kind == "uploadpack" && !observed {
				t.Fatal("the fixed upload-pack flag did not override the repository command")
			}
		})
	}
}
