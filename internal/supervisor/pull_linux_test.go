//go:build linux

package supervisor_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/wall/process"
)

func TestProcessWallPullBundleRunningAndStopped(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	probe := process.Driver{}.Probe(context.Background())
	if !probe.OK {
		t.Skipf("bubblewrap process wall unavailable: %s", probe.Reason)
	}

	th := hosttest.New(t)
	h, tr := th.ClientHost("box")

	// Set up a standalone git repo outside $HOME so process wall mounts it at /work/app.
	repo := filepath.Join(th.Root, "app")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	gitMust(t, repo, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitMust(t, repo, "add", "README.md")
	gitMust(t, repo, "commit", "-q", "-m", "initial")

	local := t.TempDir()
	gitMust(t, local, "clone", "-q", repo, ".")

	// Commit work first, THEN plant a host-canary fsmonitor in .git/config
	// targeting a host path. Neither host nor guest may run it during pull
	// (host never runs git in repo at all, and guest turns core.fsmonitor=false
	// and runs inside bwrap).
	gitMust(t, repo, "checkout", "-q", "-b", "bwrap-feat")
	if err := os.WriteFile(filepath.Join(repo, "work.txt"), []byte("from-bwrap\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitMust(t, repo, "add", "work.txt")
	gitMust(t, repo, "commit", "-q", "-m", "bwrap running commit")
	commit1 := gitMust(t, repo, "rev-parse", "HEAD")

	canary := filepath.Join(th.Home, "host-git-ran")
	gitMust(t, repo, "config", "core.fsmonitor", "touch "+canary)
	_ = os.Remove(canary)

	// Start a real --wall process session via host.New.
	info, err := host.New(host.NewArgs{
		Agent: "shell",
		Argv:  []string{"/bin/sh", "-c", "sleep 120"},
		Cwd:   repo,
		Name:  "bwrap-pull",
		Wall:  "process",
		Cols:  80,
		Rows:  24,
	})
	if err != nil {
		t.Fatalf("host.New(--wall process): %v", err)
	}
	hosttest.WaitFor(t, 15*time.Second, "process wall session running", func() bool {
		lm, err := host.LoadManifest(info.ID)
		return err == nil && lm.Status.State == host.StateWorking
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sess := &client.Session{ID: info.ID, Name: info.Name, Cwd: info.Cwd, Wall: &api.WallInfo{Level: "process", Driver: "process"}}

	// 1. Pull while running inside real bwrap process wall.
	res, err := client.PullBundleOn(ctx, tr, h, sess, client.PullBundleOptions{LocalRepo: local})
	if err != nil {
		t.Fatalf("PullBundleOn (real bwrap running): %v", err)
	}
	if res.Head != commit1 || res.Branch != "bwrap-feat" {
		t.Fatalf("bwrap running pull = %+v, want head=%s branch=bwrap-feat", res, commit1)
	}
	if _, err := os.Stat(canary); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canary %s exists! fsmonitor ran!", canary)
	}

	// 2. Stop the real process-wall session.
	stopRes, err := host.Stop(api.StopArgs{ID: info.ID})
	if err != nil {
		t.Fatalf("host.Stop: %v", err)
	}
	if stopRes.Session.State != host.StateStopped {
		t.Fatalf("after stop state = %q, want stopped", stopRes.Session.State)
	}
	if _, err := os.Stat(paths.Socket(info.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket still exists after stop: %v", err)
	}

	// 3. Commit new work (with fsmonitor off in test helper) and pull from the
	// STOPPED real bwrap sandbox!
	if err := os.WriteFile(filepath.Join(repo, "after-stop.txt"), []byte("from-stopped-bwrap\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitMust(t, repo, "-c", "core.fsmonitor=false", "add", "after-stop.txt")
	gitMust(t, repo, "-c", "core.fsmonitor=false", "commit", "-q", "-m", "bwrap stopped commit")
	commit2 := gitMust(t, repo, "-c", "core.fsmonitor=false", "rev-parse", "HEAD")
	_ = os.Remove(canary)

	res2, err := client.PullBundleOn(ctx, tr, h, sess, client.PullBundleOptions{LocalRepo: local})
	if err != nil {
		t.Fatalf("PullBundleOn (real bwrap stopped): %v", err)
	}
	if res2.Head != commit2 || res2.Ahead != "2" {
		t.Fatalf("bwrap stopped pull = %+v, want head=%s ahead=2", res2, commit2)
	}
	if msg := gitMust(t, local, "log", "-1", "--format=%s", "bwrap-feat"); msg != "bwrap stopped commit" {
		t.Fatalf("local branch head = %q, want 'bwrap stopped commit'", msg)
	}
	if _, err := os.Stat(canary); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canary %s exists after stopped pull!", canary)
	}

	// Verify session stayed stopped with zero socket and zero holder PID.
	after, err := host.LoadManifest(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status.State != host.StateStopped || after.Run.HolderPID != 0 {
		t.Fatalf("stopped pull altered manifest: state=%q pid=%d", after.Status.State, after.Run.HolderPID)
	}
	if _, err := os.Stat(paths.Socket(info.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stopped pull created session socket: %v", err)
	}
	if b, err := os.ReadFile(paths.AuditLog(info.ID)); err != nil || !strings.Contains(string(b), "stopped-sandbox") {
		t.Fatalf("audit.jsonl missing stopped-sandbox pull entry (err=%v):\n%s", err, b)
	}
}
