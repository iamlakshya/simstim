package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
)

func TestCPCommandEndToEnd(t *testing.T) {
	th := newTestHost(t)
	th.h.Install(client.HelperVersion(), "")

	work := filepath.Join(th.h.Home, "workspace")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "hello.txt"), []byte("from-session\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, ".envrc"), []byte("export BAD=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	okSaid(t, "new", "box", "--detach", "--wall", "none", "--downgrade", "--name", "work", "--dir", work, "--", "sleep", "60")

	localDir := filepath.Join(th.h.Root, "laptop")
	if err := os.MkdirAll(localDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Copy out a plain file.
	out := okSaid(t, "cp", "box/work:hello.txt", localDir)
	if !strings.Contains(out, "Copied 1 file") {
		t.Errorf("cp out = %q", out)
	}
	b, err := os.ReadFile(filepath.Join(localDir, "hello.txt"))
	if err != nil || string(b) != "from-session\n" {
		t.Fatalf("copied file = %q, %v", b, err)
	}

	// Copy out a flagged file fails without --allow-flagged (input off), succeeds with it.
	fails(t, "flagged files", "cp", "--no-input", "box/work:.envrc", localDir)
	okSaid(t, "cp", "--allow-flagged", "box/work:.envrc", localDir)

	// Copy in a local file.
	up := filepath.Join(localDir, "upload.txt")
	if err := os.WriteFile(up, []byte("from-laptop\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out = okSaid(t, "cp", up, "box/work:upload.txt")
	if !strings.Contains(out, "Copied 1 file") {
		t.Errorf("cp in = %q", out)
	}
	got, err := os.ReadFile(filepath.Join(work, "upload.txt"))
	if err != nil || string(got) != "from-laptop\n" {
		t.Fatalf("uploaded file = %q, %v", got, err)
	}

	// Usage error when neither or both sides are remote.
	fails(t, "one side of cp must be", "cp", "a.txt", "b.txt")
}

func TestSSHConfigCommandLifecycle(t *testing.T) {
	h := hosttest.New(t)
	// Put a stub ssh in PATH that rejects probe$x (simulating OpenSSH 9.6+).
	h.Script("ssh", "#!/bin/sh\nfor a in \"$@\"; do case \"$a\" in *'$'*) exit 255 ;; esac; done\nexit 0\n")
	t.Setenv("PATH", filepath.Join(h.Root, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))

	out := okSaid(t, "ssh-config", "--yes")
	if !strings.Contains(out, "Wrote ") || !strings.Contains(out, "Added Include ~/.ssh/"+brand.Name+"/config") {
		t.Errorf("ssh-config --yes = %q", out)
	}
	userCfg, err := os.ReadFile(filepath.Join(h.Home, ".ssh", "config"))
	if err != nil || !strings.Contains(string(userCfg), "Include ~/.ssh/"+brand.Name+"/config") {
		t.Fatalf("~/.ssh/config = %q, %v", userCfg, err)
	}
	out = okSaid(t, "ssh-config", "--remove")
	if !strings.Contains(out, "Removed SSH configuration") {
		t.Errorf("ssh-config --remove = %q", out)
	}
}
