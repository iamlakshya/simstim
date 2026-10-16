package host_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/alerts"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

// resume resumes a session and fails the test on an error.
func resume(t *testing.T, a api.ResumeArgs) *host.ResumeResult {
	t.Helper()
	r, err := host.Resume(a)
	if err != nil {
		t.Fatalf("resume %s: %v", a.ID, err)
	}
	return r
}

// resumeFails resumes a session, which must fail with the code and a hint.
func resumeFails(t *testing.T, a api.ResumeArgs, code string) {
	t.Helper()
	_, err := host.Resume(a)
	var pe *proto.Error
	if !errors.As(err, &pe) || pe.Code != code || pe.Hint == "" {
		t.Fatalf("resume %+v = %v, want a %s error with a hint", a, err, code)
	}
}

// After the agent ended, its holder still answers. A resume stops that
// holder and starts a new one in the same session, which runs the agent's
// own resume command after the session's resume hook. A resume is not a
// restore after a host restart.
func TestResumeAnEndedAgent(t *testing.T) {
	h := hosttest.New(t)
	fakeClaude(t, h, `[ -e "$HOME/ran" ] && exec sleep 60; touch "$HOME/ran"; exit 3`)
	// The host's own resume hook says which hook it is.
	hook := filepath.Join(paths.ConfigDir(), "hooks", "resume")
	writeFile(t, hook, "#!/bin/sh\necho \"$"+brand.Env("HOOK")+"\" > \"$HOME/hook.kind\"\n")
	if err := os.Chmod(hook, 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := host.New(host.NewArgs{Agent: "claude", Name: "app", Cwd: "~"})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, s.ID, host.StateErrored)
	old := hosttest.HolderPID(t, s.ID)
	m := manifest(t, s.ID)

	r := resume(t, api.ResumeArgs{ID: s.ID})
	if !strings.Contains(r.Why, m.Agent.AgentSessionID) || r.Session.ID != s.ID || r.Session.Name != "app" || !r.Session.Alive {
		t.Fatalf("resume = %+v", r)
	}
	if err := hosttest.Reap(old, 5*time.Second); err != nil {
		t.Errorf("the old holder still runs: %v", err)
	}
	if pid := hosttest.HolderPID(t, s.ID); pid == old {
		t.Errorf("the session kept its old holder, %d", pid)
	}
	hosttest.WaitFor(t, 5*time.Second, "claude to run its own resume command", func() bool {
		b, err := os.ReadFile(filepath.Join(h.Home, "claude.args"))
		args := strings.Split(strings.TrimSpace(string(b)), "\n")
		return err == nil && len(args) >= 3 && args[0] == "--resume" && args[1] == m.Agent.AgentSessionID && args[2] == "--settings"
	})
	hosttest.WaitFor(t, 5*time.Second, "the resume hook", func() bool {
		b, err := os.ReadFile(filepath.Join(h.Home, "hook.kind"))
		return err == nil && strings.TrimSpace(string(b)) == "resume"
	})
	m = manifest(t, s.ID)
	if m.Run.AgentPID == 0 || m.Status.State == host.StateErrored || m.Labels["resumed_at"] == "" || m.Labels["restored_at"] != "" || m.Cwd != h.Home {
		t.Errorf("after the resume: run %+v, status %+v, labels %v, folder %s", m.Run, m.Status, m.Labels, m.Cwd)
	}
	// The agent runs again, so a second resume is refused.
	resumeFails(t, api.ResumeArgs{ID: s.ID}, "agent_running")
}

// The cap is the size asked for now, else the size the session asked for
// at its start, else the host's default. A size below the smallest is
// refused before anything stops.
func TestResumeWorksOutTheCap(t *testing.T) {
	hosttest.New(t)
	s, err := host.New(host.NewArgs{Name: "capped", Argv: []string{"/bin/sh", "-c", "sleep 0.3"}, MemoryMax: api.MinMemoryMax})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		ask, asked int64
	}{
		{0, api.MinMemoryMax},
		{300 << 20, 300 << 20},
		{0, 300 << 20},
		{api.MemoryAuto, 0},
	} {
		waitState(t, s.ID, host.StateExited)
		r := resume(t, api.ResumeArgs{ID: s.ID, MemoryMax: c.ask})
		m := manifest(t, s.ID)
		if m.Limits.MemoryAsked != c.asked || m.Limits.MemoryMax != capFor(c.asked) || r.Session.MemoryMax != capFor(c.asked) {
			t.Errorf("resume asking for %d: limits %+v, row %d, want %d asked and a cap of %d", c.ask, m.Limits, r.Session.MemoryMax, c.asked, capFor(c.asked))
		}
	}
	waitState(t, s.ID, host.StateExited)
	pid := hosttest.HolderPID(t, s.ID)
	resumeFails(t, api.ResumeArgs{ID: s.ID, MemoryMax: 100 << 20}, "memory_max")
	if hosttest.HolderPID(t, s.ID) != pid {
		t.Error("a refused resume stopped the holder")
	}
}

// A running agent, a session that does not exist, and a session without
// the secrets it started with are refused. A lost session comes back,
// with the secrets the resume brings. A resume is not one of restore's
// tries after a host restart, so the count of those starts again.
func TestResumeRefusesAndBringsBackALostSession(t *testing.T) {
	hosttest.New(t)
	live := start(t, "live", "sleep", "60")
	resumeFails(t, api.ResumeArgs{ID: live.ID}, "agent_running")
	resumeFails(t, api.ResumeArgs{ID: "nosuchid"}, "not_found")
	resumeFails(t, api.ResumeArgs{ID: "../x"}, "not_found")

	s := start(t, "lost", "/bin/sh", "-c", `echo "TOKEN=$TEST_TOKEN"; exec sleep 60`)
	crash(t, s.ID)
	m := manifest(t, s.ID)
	m.Secrets = []string{"TEST_TOKEN"}
	m.Run.RespawnCount = m.Restart.MaxAttempts
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	resumeFails(t, api.ResumeArgs{ID: s.ID}, "secrets_missing")
	r := resume(t, api.ResumeArgs{ID: s.ID, Secrets: map[string]api.Secret{"TEST_TOKEN": "s3cret", "OTHER": "x"}})
	if r.Session.State == api.StateLost || !r.Session.Alive {
		t.Fatalf("the lost session did not come back: %+v", r.Session)
	}
	waitOutput(t, s.ID, "TOKEN=s3cret")
	if mm := manifest(t, s.ID); len(mm.MissingSecrets) != 0 || len(mm.Secrets) != 1 || mm.Run.RespawnCount != 0 {
		t.Errorf("after the resume: secrets %v, missing %v, restores %d", mm.Secrets, mm.MissingSecrets, mm.Run.RespawnCount)
	}
}

// A resume whose agent cannot start says why.
func TestResumeReportsAnAgentThatCannotStart(t *testing.T) {
	h := hosttest.New(t)
	s := start(t, "gone", "sleep", "60")
	// Without a holder, nothing saves the manifest over this change.
	crash(t, s.ID)
	m := manifest(t, s.ID)
	m.Agent.Argv = []string{filepath.Join(h.Home, "removed-program")}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	_, err := host.Resume(api.ResumeArgs{ID: s.ID})
	var pe *proto.Error
	if !errors.As(err, &pe) || pe.Code != "start_failed" || !strings.Contains(pe.Message, "no such file") {
		t.Fatalf("resume = %v, want start_failed with the holder's reason", err)
	}
}

// The bridge offers the resume as a feature, so a new client can tell an
// old helper. A client that does not see it says that the helper is too
// old, and sends nothing. A request that is not JSON gets a hint.
func TestBridgeResume(t *testing.T) {
	h := hosttest.New(t)
	c := dial(t, h)
	if !c.HasFeature("resume") {
		t.Fatalf("features %q, want resume", c.Features)
	}
	ctx := context.Background()
	s, err := c.NewSession(ctx, client.NewArgs{Name: "short", Argv: []string{"/bin/sh", "-c", "sleep 0.2; exit 4"}, Cwd: "~"})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, s.ID, host.StateErrored)
	rows, err := c.Sessions(ctx)
	if err != nil || len(rows) != 1 || !client.Ended(rows[0]) {
		t.Fatalf("sessions = %+v, %v", rows, err)
	}
	r, err := c.Resume(ctx, client.ResumeArgs{ID: s.ID, MemoryMax: api.MinMemoryMax})
	if err != nil || r.Session.ID != s.ID || r.Why != "started fresh" || r.Session.MemoryMax != capFor(api.MinMemoryMax) {
		t.Fatalf("resume = %+v, %v", r, err)
	}

	waitState(t, s.ID, host.StateErrored)
	pid := hosttest.HolderPID(t, s.ID)
	// An older helper does not offer it.
	c.Features = []string{"attach.replay", "mkdir"}
	_, err = c.Resume(ctx, client.ResumeArgs{ID: s.ID})
	var ue *client.UserError
	if !errors.As(err, &ue) || !strings.Contains(ue.Msg, "too old to resume") || !strings.Contains(ue.Hint, "host add box") {
		t.Fatalf("resume with an old helper = %v", err)
	}
	if hosttest.HolderPID(t, s.ID) != pid {
		t.Error("a client that saw no resume feature still resumed the session")
	}

	b := newRawBridge(t)
	b.sendJSON(proto.TRequest, proto.Request{ID: 7, Op: "resume", Args: []byte(`"not an object"`)})
	if resp := b.response(7); resp.OK || resp.Error == nil || resp.Error.Code != "bad_request" || resp.Error.Hint == "" {
		t.Fatalf("resume with bad arguments = %+v", resp)
	}
}

// Two resumes of the same session at once, as from a key pressed twice:
// one starts the agent, and the other is refused, as busy or because the
// agent runs again. One holder runs. While a resume, a restore or a delete
// holds the session's lock, the others say that it is busy.
func TestResumesTakeTurns(t *testing.T) {
	hosttest.New(t)
	s := start(t, "twice", "/bin/sh", "-c", `[ -e "$HOME/twice" ] && exec sleep 60; touch "$HOME/twice"; exit 3`)
	waitState(t, s.ID, host.StateErrored)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = host.Resume(api.ResumeArgs{ID: s.ID})
		}()
	}
	wg.Wait()
	var won int
	for _, err := range errs {
		var pe *proto.Error
		switch {
		case err == nil:
			won++
		case !errors.As(err, &pe) || (pe.Code != "busy" && pe.Code != "agent_running") || pe.Hint == "":
			t.Errorf("the second resume = %v, want busy or agent_running, with a hint", err)
		}
	}
	if won != 1 {
		t.Fatalf("%d resumes worked, want 1: %v", won, errs)
	}
	if out, err := exec.Command("pgrep", "-f", "holder "+s.ID).Output(); err == nil {
		if n := len(strings.Fields(string(out))); n != 1 {
			t.Errorf("%d holders run for one session", n)
		}
	}

	unlock, err := host.LockSession(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	crash(t, s.ID)
	resumeFails(t, api.ResumeArgs{ID: s.ID}, "busy")
	if _, err := host.Delete(s.ID); err == nil {
		t.Error("a delete went on while the session was locked")
	}
	for _, r := range host.Restore(true) {
		if r.ID == s.ID && r.Action != "skipped" {
			t.Errorf("a restore went on while the session was locked: %+v", r)
		}
	}
	unlock()
	resume(t, api.ResumeArgs{ID: s.ID})
}

// After a reboot, a restore can fail to start the agent, for example when
// its folder is not mounted yet. A later resume is still a resume: it waits
// for the secrets that it brings, and does not count as a restore.
func TestResumeAfterAFailedRestore(t *testing.T) {
	h := hosttest.New(t)
	dir := filepath.Join(h.Home, "work")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := host.New(host.NewArgs{Name: "r", Cwd: dir, Argv: []string{"/bin/sh", "-c", `echo "TOKEN=$TEST_TOKEN"; exec sleep 60`}})
	if err != nil {
		t.Fatal(err)
	}
	crash(t, s.ID)
	m := manifest(t, s.ID)
	m.Secrets = []string{"TEST_TOKEN"}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if res := host.Restore(true); len(res) != 1 || res[0].Action != "failed" {
		t.Fatalf("restore without the folder = %+v", res)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	resume(t, api.ResumeArgs{ID: s.ID, Secrets: map[string]api.Secret{"TEST_TOKEN": "s3cret"}})
	waitOutput(t, s.ID, "TOKEN=s3cret")
	if m := manifest(t, s.ID); len(m.MissingSecrets) != 0 || m.Labels["restored_at"] != "" {
		t.Errorf("after the resume: missing %v, labels %v", m.MissingSecrets, m.Labels)
	}
}

// A resumed holder sends a started alert, as a new session does, and not
// a restored one: the user's resume is not a host restart.
func TestResumedHolderSendsStarted(t *testing.T) {
	hosttest.New(t)
	got := make(chan string, 16)
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		var ev alerts.Event
		b, _ := io.ReadAll(r.Body)
		if json.Unmarshal(b, &ev) == nil {
			got <- ev.Event
		}
	}))
	t.Cleanup(srv.Close)
	if err := os.MkdirAll(filepath.Dir(paths.AlertsConfig()), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := "events = [\"started\", \"restored\"]\n[[destination]]\ntype = \"webhook\"\nurl = \"" + srv.URL + "\"\n"
	if err := os.WriteFile(paths.AlertsConfig(), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	s := start(t, "alerts", "/bin/sh", "-c", `[ -e "$HOME/again" ] && exec sleep 60; touch "$HOME/again"; exit 0`)
	next := func() string {
		select {
		case ev := <-got:
			return ev
		case <-time.After(10 * time.Second):
			return "nothing"
		}
	}
	if ev := next(); ev != alerts.Started {
		t.Fatalf("the new session sent %s, want started", ev)
	}
	waitState(t, s.ID, host.StateExited)
	resume(t, api.ResumeArgs{ID: s.ID})
	if ev := next(); ev != alerts.Started {
		t.Fatalf("the resumed session sent %s, want started", ev)
	}
}
