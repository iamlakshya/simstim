package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/paths"
)

func initP116Repo(t *testing.T, dir string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	for k, v := range map[string]string{
		"GIT_AUTHOR_NAME":     "test",
		"GIT_AUTHOR_EMAIL":    "test@example.com",
		"GIT_COMMITTER_NAME":  "test",
		"GIT_COMMITTER_EMAIL": "test@example.com",
		"GIT_CONFIG_GLOBAL":   os.DevNull,
	} {
		t.Setenv(k, v)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit := func(args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	runGit("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "README.md")
	runGit("commit", "-q", "-m", "initial")
}

// Non-TTY `new box --seed <repo> --rm -- <command>` runs through exec, streams
// stdout/stderr, exits with the command's exit code, and removes the session
// when no unpulled commits remain (or stops it with a message when commits were
// created inside the session) (isolation spec 17.6, plan P1.16).
func TestNewNonTTYExecWithSeedAndRM(t *testing.T) {
	th := newTestHost(t)
	seedRepo := filepath.Join(t.TempDir(), "demo")
	initP116Repo(t, seedRepo)

	// Case 1: clean exit 0 -> prints stdout and removes session.
	out, errOut, code := mainRun(t, "new", "box", "--seed", seedRepo, "--rm", "--", "/bin/sh", "-c", "echo EXEC_STDOUT; cat README.md")
	if code != 0 || !strings.Contains(out, "EXEC_STDOUT") || !strings.Contains(out, "hello") {
		t.Fatalf("new --seed --rm (exit 0): code=%d out=%q err=%q", code, out, errOut)
	}
	if list := liveList(t, th); len(list) != 0 {
		t.Fatalf("session was not removed after clean --rm exit: %+v", list)
	}

	// Case 2: non-zero exit code -> propagates exit code and removes clean session.
	out, errOut, code = mainRun(t, "new", "box", "--seed", seedRepo, "--rm", "--", "/bin/sh", "-c", "echo FAIL_OUT; echo FAIL_ERR >&2; exit 7")
	if code != 7 || !strings.Contains(out, "FAIL_OUT") || !strings.Contains(errOut, "FAIL_ERR") {
		t.Fatalf("new --seed --rm (exit 7): code=%d out=%q err=%q", code, out, errOut)
	}
	if list := liveList(t, th); len(list) != 0 {
		t.Fatalf("session was not removed after failing --rm exit: %+v", list)
	}

	// Case 3: unpulled commits inside the session -> stops instead of removing and says so.
	commitCmd := `git config user.name test && git config user.email test@example.com && echo change > new.txt && git add new.txt && git commit -q -m c1 && echo change2 > new2.txt && git add new2.txt && git commit -q -m c2`
	out, errOut, code = mainRun(t, "new", "box", "--name", "auth-fix", "--seed", seedRepo, "--rm", "--", "/bin/sh", "-c", commitCmd)
	if code != 0 {
		t.Fatalf("new --rm with commits: code=%d out=%q err=%q", code, out, errOut)
	}
	wantNote := "auth-fix has 2 commits that you have not pulled, so it stopped instead of being removed."
	if !strings.Contains(errOut, wantNote) {
		t.Fatalf("stderr = %q, want %q", errOut, wantNote)
	}
	list := liveList(t, th)
	if len(list) != 1 || list[0].State != api.StateStopped || list[0].Name != "auth-fix" {
		t.Fatalf("sessions after unpulled --rm = %+v, want 1 stopped session", list)
	}

	// Case 4: non-TTY `new -- <command>` without `--rm` runs the command, keeps
	// the workspace on disk, and stops the placeholder session instead of
	// leaving a live sleep process running.
	out, errOut, code = mainRun(t, "new", "box", "--name", "kept-run", "--seed", seedRepo, "--", "/bin/sh", "-c", "echo KEPT_STDOUT; echo saved > kept.txt")
	if code != 0 || !strings.Contains(out, "KEPT_STDOUT") {
		t.Fatalf("new --seed without --rm: code=%d out=%q err=%q", code, out, errOut)
	}
	var kept *client.Session
	for _, s := range liveList(t, th) {
		if s.Name == "kept-run" {
			cp := s
			kept = &cp
			break
		}
	}
	if kept == nil {
		t.Fatal("kept-run session missing after non-TTY new without --rm")
	}
	if kept.State != api.StateStopped || kept.Alive {
		t.Fatalf("kept-run session = state=%q alive=%v, want stopped and not alive", kept.State, kept.Alive)
	}
	if data, err := os.ReadFile(filepath.Join(kept.Cwd, "kept.txt")); err != nil || strings.TrimSpace(string(data)) != "saved" {
		t.Fatalf("kept.txt in %s = %q (err=%v), want saved", kept.Cwd, string(data), err)
	}
}

// Interactive attached `new --rm` must not race fast-exiting commands (deleting
// the session before attach connects), and must still auto-remove on the host
// when the user attaches, detaches while running, and the command exits later.
func TestNewAttachedRMFastExitAndDetach(t *testing.T) {
	t.Run("fast_exit", func(t *testing.T) {
		th := newTestHost(t)
		for i := range 3 {
			t.Run(fmt.Sprintf("run_%d", i), func(t *testing.T) {
				term, done := inTerminal(t, "new", "box", "--name", "fast-rm", "--rm", "--", "/bin/sh", "-c", "echo FAST_RM_OK; exit 7")
				err := wait(t, done)
				var se silentError
				if !errors.As(err, &se) || se.code != 7 {
					t.Fatalf("attached fast-exit new --rm err = %v, want exit code 7 (output=%q)", err, term.Output())
				}
				term.WaitFor("FAST_RM_OK")
				if list := liveList(t, th); len(list) != 0 {
					t.Fatalf("sessions left after attached fast-exit --rm: %+v", list)
				}
			})
		}
	})

	t.Run("detach_then_exit", func(t *testing.T) {
		th := newTestHost(t)
		marker := filepath.Join(t.TempDir(), "release")
		term, done := inTerminal(t, "new", "box", "--name", "detach-rm", "--rm", "--",
			"/bin/sh", "-c", `echo DETACH_RM_OK; while [ ! -f "$0" ]; do sleep 0.05; done`, marker)
		term.WaitFor("DETACH_RM_OK")
		term.Type("\x1c")
		if err := wait(t, done); err != nil {
			t.Fatalf("detach from new --rm: %v", err)
		}
		term.WaitFor("Detached. detach-rm keeps running on box.")
		if list := liveList(t, th); len(list) != 1 || !list[0].Alive {
			t.Fatalf("session should remain alive while detached before exit: %+v", list)
		}
		if err := os.WriteFile(marker, []byte("go\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		hosttest.WaitFor(t, 10*time.Second, "detached --rm session removed after exit", func() bool {
			return len(liveList(t, th)) == 0
		})
	})
}

// `new --count 4 --detach --json` prints one array of 4 session objects. When
// the host refuses one, its array entry carries the error object, the others
// start, and the command exits with the refused error's exit code (spec 17.6,
// cli-spec 7.1).
func TestNewCountJSONAndPartialRefusal(t *testing.T) {
	th := newTestHost(t)

	out, errOut, code := mainRun(t, "new", "box", "--name", "worker", "--count", "4", "--detach", "--json", "--", "sleep", "60")
	if code != 0 || errOut != "" {
		t.Fatalf("new --count 4: code=%d out=%q err=%q", code, out, errOut)
	}
	checkJSON(t, "new --count 4 --detach --json", out, false)
	var items []map[string]any
	if err := json.Unmarshal([]byte(out), &items); err != nil || len(items) != 4 {
		t.Fatalf("decoded items = %v (err=%v), want 4", items, err)
	}
	for i, wantName := range []string{"worker-1", "worker-2", "worker-3", "worker-4"} {
		if items[i]["name"] != wantName || items[i]["v"] != float64(1) || items[i]["host"] != "box" {
			t.Errorf("item[%d] = %v, want name=%q v=1 host=box", i, items[i], wantName)
		}
	}
	if list := liveList(t, th); len(list) != 4 {
		t.Fatalf("live sessions = %d, want 4", len(list))
	}

	// Simulate host refusal (exit 5, KindRefused) on the 3rd session of a 4-session batch.
	var calls atomic.Int32
	orig := startCountSession
	startCountSession = func(cmd *cobra.Command, conn *client.Conn, h client.Host, a client.NewArgs, prep *preparedNewP114) (*client.Session, error) {
		if calls.Add(1) == 3 {
			return nil, &client.UserError{
				Msg:  "not enough room on box for " + a.Name,
				Hint: "Stop or remove unused sessions first.",
				Code: "no_room",
				Kind: client.KindRefused,
			}
		}
		return startNewWithP114(cmd, conn, h, a, prep)
	}
	t.Cleanup(func() { startCountSession = orig })

	out, errOut, code = mainRun(t, "new", "box", "--name", "batch", "--count", "4", "--json", "--", "sleep", "60")
	if code != 5 || errOut != "" {
		t.Fatalf("partial refusal: code=%d (want 5) out=%q err=%q", code, out, errOut)
	}
	checkJSON(t, "new --count 4 partial refusal", out, false)
	var partial []map[string]any
	if err := json.Unmarshal([]byte(out), &partial); err != nil || len(partial) != 4 {
		t.Fatalf("partial items = %v (err=%v), want 4", partial, err)
	}
	errEntry := partial[2]
	errObj, ok := errEntry["error"].(map[string]any)
	if !ok || errEntry["name"] != "batch-3" || errObj["code"] != "no_room" || errObj["exit"] != float64(5) {
		t.Fatalf("partial[2] = %v, want name=batch-3 with error.code=no_room exit=5", errEntry)
	}
	for _, idx := range []int{0, 1, 3} {
		if partial[idx]["id"] == "" || partial[idx]["error"] != nil {
			t.Errorf("partial[%d] = %v, want started session", idx, partial[idx])
		}
	}

	// Invalid --count fails with exit code 2.
	if _, _, code := mainRun(t, "new", "box", "--count", "0"); code != 2 {
		t.Errorf("--count 0 exited %d, want 2", code)
	}
}

func TestPruneCommand(t *testing.T) {
	newTestHost(t)
	now := time.Now().UTC()

	makeStopped := func(id, name string, stoppedAt time.Time, dirty bool) {
		t.Helper()
		repoDir := filepath.Join(paths.WorkDir(id), "repo")
		initP116Repo(t, repoDir)
		c := exec.Command("git", "rev-parse", "HEAD")
		c.Dir = repoDir
		baseOut, err := c.Output()
		if err != nil {
			t.Fatal(err)
		}
		baseSHA := strings.TrimSpace(string(baseOut))
		if dirty {
			_ = os.WriteFile(filepath.Join(repoDir, "extra.txt"), []byte("x\n"), 0o644)
			for _, args := range [][]string{{"add", "extra.txt"}, {"commit", "-q", "-m", "extra"}} {
				gc := exec.Command("git", args...)
				gc.Dir = repoDir
				if out, err := gc.CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v\n%s", args, err, out)
				}
			}
		}
		m := &host.Manifest{
			Schema:    1,
			ID:        id,
			Name:      name,
			CreatedAt: stoppedAt.Add(-time.Hour),
			Cwd:       repoDir,
			Agent:     host.AgentInfo{Kind: "shell"},
			Status:    host.Status{State: host.StateStopped, Since: stoppedAt},
			SeedRepos: []api.SeedRepoRecord{{Name: "repo", Dir: repoDir, Branch: "main", SeedHead: baseSHA}},
		}
		if err := m.Save(); err != nil {
			t.Fatal(err)
		}
	}

	makeStopped("prn23456", "old-clean", now.Add(-10*24*time.Hour), false)
	makeStopped("drt23456", "old-dirty", now.Add(-10*24*time.Hour), true)
	makeStopped("rec23456", "recent-clean", now.Add(-1*time.Hour), false)

	// --dry-run --json lists candidates and kept sessions without removing.
	out, errOut, code := mainRun(t, "prune", "box", "--stopped-before", "7d", "--dry-run", "--json")
	if code != 0 || errOut != "" {
		t.Fatalf("prune --dry-run --json: code=%d out=%q err=%q", code, out, errOut)
	}
	checkJSON(t, "prune --dry-run --json", out, false)
	if !strings.Contains(out, `"would_remove"`) || !strings.Contains(out, `"kept"`) || strings.Contains(out, "recent-clean") {
		t.Fatalf("prune dry-run output unexpected:\n%s", out)
	}

	// Without -y and with input off (--no-input), prune fails with exit code 4 naming -y.
	_, errOut, code = mainRun(t, "prune", "box", "--stopped-before", "7d", "--no-input")
	if code != 4 || !strings.Contains(errOut, "-y") {
		t.Fatalf("prune --no-input without -y: code=%d (want 4) err=%q", code, errOut)
	}

	// With -y --json, removes old-clean, keeps old-dirty, leaves recent-clean untouched.
	out, errOut, code = mainRun(t, "prune", "box", "--stopped-before", "7d", "-y", "--json")
	if code != 0 || errOut != "" {
		t.Fatalf("prune -y --json: code=%d out=%q err=%q", code, out, errOut)
	}
	checkJSON(t, "prune -y --json", out, false)
	if !strings.Contains(out, `"removed"`) || !strings.Contains(out, `"kept"`) {
		t.Fatalf("prune -y output unexpected:\n%s", out)
	}
	if _, err := host.LoadManifest("prn23456"); err == nil {
		t.Fatal("old-clean was not removed")
	}
	if _, err := host.LoadManifest("drt23456"); err != nil {
		t.Fatal("old-dirty was removed despite unpulled commits")
	}
	if _, err := host.LoadManifest("rec23456"); err != nil {
		t.Fatal("recent-clean was removed despite --stopped-before 7d")
	}
}

func TestEventsGoldenLinesAndStream(t *testing.T) {
	base := time.Date(2026, 11, 28, 15, 4, 0, 0, time.UTC)
	zero := 0
	samples := []api.WatchEvent{
		{Event: api.EventStateChanged, Host: "box", Session: "abcd2345", Name: "auth-fix", Time: base.Add(1 * time.Second), State: "working", Prev: "idle"},
		{Event: api.EventNeedsInput, Host: "box", Session: "abcd2345", Name: "auth-fix", Time: base.Add(2 * time.Second), Detail: "Approve tool call?"},
		{Event: api.EventTurnDone, Host: "box", Session: "abcd2345", Name: "auth-fix", Time: base.Add(3 * time.Second)},
		{Event: api.EventEnded, Host: "box", Session: "abcd2345", Name: "auth-fix", Time: base.Add(4 * time.Second), ExitCode: &zero},
		{Event: api.EventEgressHeld, Host: "box", Session: "abcd2345", Name: "auth-fix", Time: base.Add(5 * time.Second), Target: "api.example.com:443", Detail: "waiting for approval"},
		{Event: api.EventClassifierAllow, Host: "box", Session: "abcd2345", Name: "auth-fix", Time: base.Add(6 * time.Second), Target: "registry.npmjs.org:443", Detail: "allowed by policy"},
		{Event: api.EventSandboxReady, Host: "box", Session: "abcd2345", Name: "auth-fix", Time: base.Add(7 * time.Second)},
		{Event: api.EventSandboxStopped, Host: "box", Session: "abcd2345", Name: "auth-fix", Time: base.Add(8 * time.Second), Reason: "max_run"},
		{Event: api.EventSandboxError, Host: "box", Session: "abcd2345", Name: "auth-fix", Time: base.Add(9 * time.Second), Detail: "wall process exited"},
	}

	var b strings.Builder
	for _, ev := range samples {
		b.WriteString(formatEventLine(ev, time.UTC))
		b.WriteByte('\n')
	}
	golden := filepath.Join("testdata", "events.golden")
	if *update {
		if err := os.WriteFile(golden, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if b.String() != string(want) {
		t.Errorf("events lines differ from %s:\ngot:\n%s\nwant:\n%s", golden, b.String(), string(want))
	}

	// Real snapshot `events box --json` through test host bridge.
	th := newTestHost(t)
	host.RecordWatchEvent(api.WatchEvent{Event: api.EventSandboxReady, Session: "evnt2345", Name: "app"})
	host.RecordWatchEvent(api.WatchEvent{Event: api.EventNeedsInput, Session: "evnt2345", Name: "app", Detail: "Confirm?"})

	out, errOut, code := mainRun(t, "events", "box", "--json")
	if code != 0 || errOut != "" {
		t.Fatalf("events box --json: code=%d out=%q err=%q", code, out, errOut)
	}
	checkJSON(t, "events box --json", out, false)
	if !strings.Contains(out, `"sandbox_ready"`) || !strings.Contains(out, `"needs_input"`) {
		t.Fatalf("events box --json missing expected events:\n%s", out)
	}

	// Real streaming `events box --follow --json` with latency check.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	before := len(th.tr.Remotes())
	gotCh := make(chan api.WatchEvent, 4)
	errCh := make(chan error, 1)
	go func() {
		errCh <- client.WatchEvents(ctx, th.ch, api.EventsWatchArgs{Follow: true}, func(ev api.WatchEvent) error {
			gotCh <- ev
			return nil
		})
	}()
	hosttest.WaitFor(t, 5*time.Second, "events.watch bridge started", func() bool {
		return len(th.tr.Remotes()) > before
	})
	// Send a warm-up event until the bridge is subscribed, then time live delivery.
	ready := false
	readyDeadline := time.Now().Add(5 * time.Second)
	for !ready {
		if time.Now().After(readyDeadline) {
			t.Fatal("timed out waiting for events.watch subscription warmup")
		}
		host.RecordWatchEvent(api.WatchEvent{Event: api.EventSandboxReady, Session: "evnt2345", Name: "app"})
		select {
		case err := <-errCh:
			t.Fatalf("client.WatchEvents exited early: %v", err)
		case <-gotCh:
			ready = true
		case <-time.After(50 * time.Millisecond):
		}
	}

	sentAt := time.Now()
	host.RecordWatchEvent(api.WatchEvent{Event: api.EventNeedsInput, Session: "evnt2345", Name: "app", Detail: "Live prompt"})
	for {
		select {
		case err := <-errCh:
			t.Fatalf("client.WatchEvents exited early: %v", err)
		case ev := <-gotCh:
			if ev.Event != api.EventNeedsInput {
				continue
			}
			lat := time.Since(sentAt)
			if lat > 200*time.Millisecond {
				t.Fatalf("events --follow latency = %v, want <= 200ms", lat)
			}
			var lineBuf bytes.Buffer
			if err := writeEventJSONLine(&lineBuf, ev); err != nil {
				t.Fatal(err)
			}
			var obj map[string]any
			if err := json.Unmarshal(lineBuf.Bytes(), &obj); err != nil || obj["v"] != float64(1) || obj["event"] != api.EventNeedsInput {
				t.Fatalf("event JSON line = %q (err=%v)", lineBuf.String(), err)
			}
			cancel()
			<-errCh
			return
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for live event on client.WatchEvents")
		}
	}
}

func TestConfigCheckSandboxMaxRun(t *testing.T) {
	hosttest.New(t)
	writeSettings(t, "[sandbox]\nmax_run = \"1m\"\n")
	if out, errOut, code := mainRun(t, "config", "check", "--json"); code != 0 {
		t.Fatalf("valid max_run failed: code=%d out=%q err=%q", code, out, errOut)
	}
	writeSettings(t, "[sandbox]\nmax_run = \"never-ever\"\n")
	if out, _, code := mainRun(t, "config", "check", "--json"); code == 0 || !strings.Contains(out, "sandbox.max_run") {
		t.Fatalf("invalid max_run did not fail: code=%d out=%q", code, out)
	}
}
