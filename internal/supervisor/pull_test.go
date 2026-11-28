package supervisor_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/supervisor"
)

func gitMust(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestSupervisorPullBundleRunningAndStopped(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	th := hosttest.New(t)
	h, tr := th.ClientHost("box")

	work := filepath.Join(th.Home, "repo")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	gitMust(t, work, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(work, "base.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitMust(t, work, "add", "base.txt")
	gitMust(t, work, "commit", "-q", "-m", "base")
	baseSHA := gitMust(t, work, "rev-parse", "HEAD")

	local := t.TempDir()
	gitMust(t, local, "clone", "-q", work, ".")

	// Work on a feature branch inside the session's workspace, plus an
	// unrequested tag that must never cross into the local clone.
	gitMust(t, work, "checkout", "-q", "-b", "feat")
	if err := os.WriteFile(filepath.Join(work, "clean.txt"), []byte("from-guest\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitMust(t, work, "add", "clean.txt")
	gitMust(t, work, "commit", "-q", "-m", "guest commit 1")
	gitMust(t, work, "tag", "guest-only-tag")
	commit1 := gitMust(t, work, "rev-parse", "HEAD")

	const id = "wpul2345"
	now := time.Now().UTC()
	m := &host.Manifest{
		Schema:    1,
		ID:        id,
		Name:      "repo",
		CreatedAt: now,
		Agent:     host.AgentInfo{Kind: "shell", Argv: []string{"/bin/sh", "-c", "sleep 60"}},
		Cwd:       work,
		Env:       []string{"HOME=" + th.Home, "PATH=" + os.Getenv("PATH"), "TERM=xterm-256color"},
		Term:      host.TermInfo{Term: "xterm-256color", Cols: 80, Rows: 24},
		Run:       host.Runtime{Socket: paths.Socket(id), BootID: host.BootID()},
		Status:    host.Status{State: host.StateStarting, Since: now},
		SeedHeads: map[string]string{work: baseSHA},
		Wall: &host.WallRecord{
			Level:          "process",
			Driver:         "plain",
			WorkspaceHost:  work,
			WorkspaceGuest: work,
		},
	}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}

	sb := &inProcSandbox{runDir: paths.GuestRuntimeDir(id)}
	supDone := make(chan error, 1)
	go func() {
		supDone <- supervisor.RunWith(id, sb, supervisor.Options{
			HeartbeatTimeout: 10 * time.Second,
			TickInterval:     200 * time.Millisecond,
			StopGrace:        time.Second,
		})
	}()
	stopped := false
	stopSup := func() {
		if stopped {
			return
		}
		stopped = true
		if _, err := host.HolderRequest(id, "kill", nil, 3*time.Second); err != nil {
			t.Logf("HolderRequest(kill): %v", err)
		}
		select {
		case err := <-supDone:
			if err != nil {
				t.Logf("supervisor.RunWith returned: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Logf("timed out waiting for supervisor.RunWith to exit")
			if b, err := os.ReadFile(paths.SupervisorLog(id)); err == nil {
				t.Logf("supervisor log:\n%s", b)
			}
		}
	}
	t.Cleanup(func() {
		stopSup()
		_ = os.RemoveAll(paths.SessionDir(id))
	})

	hosttest.WaitFor(t, 10*time.Second, "supervisor running", func() bool {
		lm, err := host.LoadManifest(id)
		return err == nil && lm.Status.State == host.StateWorking
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	sess := &client.Session{ID: id, Name: "repo", Cwd: work, Wall: &api.WallInfo{Level: "process"}}

	// 1. Pull while running: flows via supervisor -> link.KindBundle -> guest.
	res, err := client.PullBundleOn(ctx, tr, h, sess, client.PullBundleOptions{LocalRepo: local})
	if err != nil {
		t.Fatalf("PullBundleOn (running): %v", err)
	}
	if res.Head != commit1 || res.Branch != "feat" || res.Into != "feat" || res.Ahead != "1" {
		t.Fatalf("running pull res = %+v, want head=%s", res, commit1)
	}
	if _, err := os.Stat(filepath.Join(local, "clean.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pull checked out clean.txt into local working tree")
	}
	if tags := gitMust(t, local, "tag", "-l"); tags != "" {
		t.Fatalf("tags leaked into local repo: %q", tags)
	}

	// 2. RecordPulled via bridge updates the live supervisor manifest & audit log.
	conn, err := client.Dial(ctx, h, false)
	if err != nil {
		t.Fatalf("client.Dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	if !conn.HasFeature(api.FeaturePulled) {
		t.Fatalf("bridge missing feature %q", api.FeaturePulled)
	}
	if err := conn.RecordPulled(ctx, id, []string{commit1}); err != nil {
		t.Fatalf("RecordPulled: %v", err)
	}
	lm, err := host.LoadManifest(id)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(lm.Pulled, commit1) {
		t.Fatalf("manifest.Pulled = %v, want %s", lm.Pulled, commit1)
	}

	// 3. Oversized bundle cap (pull_max) is enforced by guest & supervisor.
	_, err = client.PullBundleOn(ctx, tr, h, sess, client.PullBundleOptions{
		LocalRepo: local,
		MaxBytes:  32,
	})
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Code != "pull_too_big" {
		t.Fatalf("tiny MaxBytes pull = %v, want pull_too_big", err)
	}

	// 4. Stop the supervisor and mark session stopped; verify pull works from
	// a stopped walled sandbox via a temporary bundle-only sandbox and leaves
	// the session stopped with no socket or running agent.
	stopSup()
	lm, err = host.LoadManifest(id)
	if err != nil {
		t.Fatal(err)
	}
	lm.Run.HolderPID = 0
	lm.Run.AgentPID = 0
	lm.Status = host.Status{State: host.StateStopped, Since: time.Now().UTC(), Source: "user", Reason: "stopped"}
	if err := lm.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(paths.Socket(id)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected session socket removed after stop, stat err=%v", err)
	}

	// Add a second commit while stopped.
	if err := os.WriteFile(filepath.Join(work, "stopped.txt"), []byte("while-stopped\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitMust(t, work, "add", "stopped.txt")
	gitMust(t, work, "commit", "-q", "-m", "commit while stopped")
	commit2 := gitMust(t, work, "rev-parse", "HEAD")

	// Inject inProcSandbox factory to verify the temporary bundle-only sandbox
	// lifecycle cleanly cross-platform.
	var factoryCalls int
	prevFactory := supervisor.BundleSandboxFactory
	supervisor.BundleSandboxFactory = func(_ *host.Manifest, tmpDir string) (supervisor.Sandbox, error) {
		factoryCalls++
		return &inProcSandbox{runDir: filepath.Join(tmpDir, "guest")}, nil
	}
	t.Cleanup(func() { supervisor.BundleSandboxFactory = prevFactory })

	// Uncommitted dirty file should be reported without failing pull.
	if err := os.WriteFile(filepath.Join(work, "dirty.txt"), []byte("uncommitted\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	res2, err := client.PullBundleOn(ctx, tr, h, sess, client.PullBundleOptions{LocalRepo: local})
	if err != nil {
		t.Fatalf("PullBundleOn (stopped): %v", err)
	}
	if factoryCalls != 1 {
		t.Errorf("BundleSandboxFactory calls = %d, want 1", factoryCalls)
	}
	if res2.Head != commit2 || res2.Dirty != 1 || res2.Ahead != "2" {
		t.Fatalf("stopped pull res = %+v, want head=%s dirty=1 ahead=2", res2, commit2)
	}
	if msg := gitMust(t, local, "log", "-1", "--format=%s", "feat"); msg != "commit while stopped" {
		t.Fatalf("local feat head = %q, want 'commit while stopped'", msg)
	}
	// Session must STILL be stopped, with no socket recreated!
	after, err := host.LoadManifest(id)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status.State != host.StateStopped {
		t.Fatalf("stopped pull changed session state to %q, want %q", after.Status.State, host.StateStopped)
	}
	if _, err := os.Stat(paths.Socket(id)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stopped pull left session socket behind (stat err=%v)", err)
	}

	// Verify audit.jsonl recorded both running pull, pulled, and stopped-sandbox pull.
	auditBytes, err := os.ReadFile(paths.AuditLog(id))
	if err != nil {
		t.Fatalf("read audit.jsonl: %v", err)
	}
	auditText := string(auditBytes)
	for _, want := range []string{`"event":"pull"`, `"event":"pulled"`, `stopped-sandbox`} {
		if !strings.Contains(auditText, want) {
			t.Errorf("audit.jsonl missing %s:\n%s", want, auditText)
		}
	}
}

func TestPulledOpRefusedInsideSession(t *testing.T) {
	th := hosttest.New(t)
	h, _ := th.ClientHost("box")
	const id = "pins2345"
	m := &host.Manifest{
		Schema:    1,
		ID:        id,
		Name:      "inside-test",
		CreatedAt: time.Now().UTC(),
		Cwd:       th.Home,
		Agent:     host.AgentInfo{Kind: "shell", Argv: []string{"/bin/sh"}},
	}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(paths.SessionDir(id)) })

	hosttest.InSession(t, id)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := client.Dial(ctx, h, false)
	if err != nil {
		t.Fatalf("client.Dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	err = conn.RecordPulled(ctx, id, []string{strings.Repeat("a", 40)})
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Code != "forbidden" {
		t.Fatalf("RecordPulled inside session = %v, want forbidden", err)
	}
}
