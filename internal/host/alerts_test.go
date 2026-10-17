package host

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/alerts"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

// alertHome gives a test its own config and state folders.
func alertHome(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	t.Setenv("SHELL", "/bin/sh")
}

func TestPersonTyped(t *testing.T) {
	for in, want := range map[string]bool{
		"y":                           true,
		"\r":                          true,
		"\x1b[A":                      true,
		"\x1b":                        true, // a lone Escape key
		"\x1b[?62;22c":                false,
		"\x1b[I\x1b[O":                false,
		"\x1b]11;rgb:0/0/0\x1b\\":     false,
		"\x1b[12;40Rx":                true,
		"\x1b[?1u\x1b[>1;10;0c":       false,
		"\x1b[<0;3;4M":                true, // a click is a person
		"\x1b[200~pasted\x1b[201~":    true,
		"":                            false,
		"\x1b[8;24;80t\x1b[?2026;2$y": false,
	} {
		if got := personTyped([]byte(in)); got != want {
			t.Errorf("personTyped(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestWasRestored(t *testing.T) {
	start := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	m := &Manifest{Run: Runtime{StartedAt: start}}
	if wasRestored(m) {
		t.Error("a new session counts as restored")
	}
	m.Labels = map[string]string{"restored_at": start.Add(time.Hour).Format(time.RFC3339)}
	if !wasRestored(m) {
		t.Error("a restored session does not count as restored")
	}
	m.Run.StartedAt = start.Add(2 * time.Hour)
	if wasRestored(m) {
		t.Error("an old restore label counts after a later start")
	}
	// The user's resume after that start is not a restore: the session sends
	// a started alert, and waits for the secrets that the client sends.
	m.Labels[resumedLabel] = start.Add(3 * time.Hour).Format(time.RFC3339Nano)
	if wasRestored(m) || !wasResumed(m) {
		t.Errorf("after a resume: restored %v, resumed %v", wasRestored(m), wasResumed(m))
	}
}

// alertHolder is a holder with a dispatcher that posts to a test server.
func alertHolder(t *testing.T, cfg string) (*Holder, chan alerts.Event) {
	t.Helper()
	alertHome(t)
	got := make(chan alerts.Event, 16)
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		var ev alerts.Event
		b, _ := io.ReadAll(r.Body)
		if json.Unmarshal(b, &ev) == nil {
			got <- ev
		}
	}))
	t.Cleanup(srv.Close)
	if err := os.MkdirAll(filepath.Dir(paths.AlertsConfig()), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg = strings.ReplaceAll(cfg, "URL", srv.URL)
	if err := os.WriteFile(paths.AlertsConfig(), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	h := &Holder{id: "abc123", log: slog.New(slog.DiscardHandler),
		m: &Manifest{Name: "auth-fix", Agent: AgentInfo{Kind: "claude"}, Cwd: "/src"}}
	h.startAlerts(false)
	t.Cleanup(func() { h.al.d.Close(time.Second) })
	return h, got
}

func expectEvent(t *testing.T, got chan alerts.Event, kind string) alerts.Event {
	t.Helper()
	select {
	case ev := <-got:
		if ev.Event != kind {
			t.Fatalf("got %s, want %s", ev.Event, kind)
		}
		return ev
	case <-time.After(5 * time.Second):
		t.Fatalf("no %s", kind)
	}
	return alerts.Event{}
}

func TestHolderRaisesEvents(t *testing.T) {
	h, got := alertHolder(t, `events = ["started", "needs_input", "turn_done", "failed", "oom", "exited"]
delay = "0s"
min_turn = "1m"
detail = true
[[destination]]
type = "webhook"
url = "URL"
`)
	if ev := expectEvent(t, got, alerts.Started); ev.Session.Name != "auth-fix" || ev.Session.ID != "abc123" || ev.Session.Agent != "claude" {
		t.Errorf("started = %+v", ev)
	}

	h.mu.Lock()
	// A turn of two working spells, around a question: only working time counts.
	h.al.workSince = time.Now().Add(-50 * time.Second)
	h.notifyMsg = "Claude needs your permission to use Bash"
	h.alertState(StateWorking, StateNeedsInput)
	h.mu.Unlock()
	if ev := expectEvent(t, got, alerts.NeedsInput); ev.Detail != "Claude needs your permission to use Bash" {
		t.Errorf("detail = %q", ev.Detail)
	}

	h.mu.Lock()
	h.notifyMsg = ""
	h.alertState(StateNeedsInput, StateWorking)
	h.al.workSince = time.Now().Add(-20 * time.Second)
	h.alertState(StateWorking, StateIdle)
	h.mu.Unlock()
	if ev := expectEvent(t, got, alerts.TurnDone); !strings.Contains(ev.Summary, "finished a turn (1m10s)") {
		t.Errorf("turn_done = %q", ev.Summary)
	}

	h.mu.Lock()
	h.alertState(StateIdle, StateWorking)
	h.alertState(StateWorking, StateIdle) // a short turn: no turn_done
	h.alertEnded(proto.Ended{ExitCode: 3})
	h.mu.Unlock()
	if ev := expectEvent(t, got, alerts.Failed); ev.ExitCode == nil || *ev.ExitCode != 3 || ev.Summary != "auth-fix on "+ev.Host+" failed (exit 3)" {
		t.Errorf("failed = %+v", ev)
	}

	h.mu.Lock()
	h.alertEnded(proto.Ended{ExitCode: 137, Signal: "killed", OOM: true})
	h.alertEnded(proto.Ended{ExitCode: 0})
	h.mu.Unlock()
	expectEvent(t, got, alerts.OOM)
	if ev := expectEvent(t, got, alerts.Exited); *ev.ExitCode != 0 {
		t.Errorf("exited = %+v", ev)
	}

	// Once the holder stops the session itself, nothing more goes out.
	h.closeAlerts()
	h.mu.Lock()
	h.alertEnded(proto.Ended{ExitCode: 129, Signal: "hangup"})
	h.mu.Unlock()
	select {
	case ev := <-got:
		t.Errorf("%s was sent after the holder stopped the session", ev.Event)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestAlertsOps(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hosts are Linux")
	}
	alertHome(t)
	if info := AlertsGet(); info.Exists || info.Config != nil || info.Error != "" {
		t.Errorf("no file: %+v", info)
	}
	if _, err := AlertsTest(api.AlertsTestArgs{}); err == nil || !strings.Contains(err.Error(), "off") {
		t.Errorf("test with no file = %v", err)
	}
	out := filepath.Join(t.TempDir(), "events.jsonl")
	set := api.AlertsSetArgs{Config: api.AlertsConfig{Delay: "2s", Destinations: []api.AlertDestination{{Type: "command", Run: "cat >> " + out}}}}
	info, err := AlertsSet(set)
	if err != nil || !info.Exists || info.Config == nil || info.Config.Delay != "2s" {
		t.Fatalf("set = %+v, %v", info, err)
	}
	if st, err := os.Stat(paths.AlertsConfig()); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("the file is not private: %v %v", st.Mode(), err)
	}
	add := api.AlertsSetArgs{Add: true, Config: api.AlertsConfig{Destinations: []api.AlertDestination{{Type: "ntfy", URL: "https://127.0.0.1:1/topic-123", Token: "tk_test"}}}}
	info, err = AlertsSet(add)
	if err != nil || len(info.Config.Destinations) != 2 || info.Config.Delay != "2s" {
		t.Fatalf("add = %+v, %v", info, err)
	}
	if d := info.Config.Destinations[1]; d.Token != alerts.Hidden || strings.Contains(d.URL, "topic-123") {
		t.Errorf("get shows a secret: %+v", d)
	}
	if _, err := AlertsSet(api.AlertsSetArgs{Config: api.AlertsConfig{Events: []string{"nope"}}}); err == nil {
		t.Error("a bad config was written")
	}
	var pe *proto.Error
	if _, err := AlertsTest(api.AlertsTestArgs{Event: "nope"}); err == nil || !asProto(err, &pe) || pe.Hint == "" {
		t.Errorf("an unknown test event = %v", err)
	}

	// Only the command can succeed here: nothing answers on port 1.
	res, err := AlertsTest(api.AlertsTestArgs{Event: "failed"})
	if err != nil || len(res) != 2 || !res[0].OK || res[0].Type != "command" {
		t.Fatalf("test = %+v, %v", res, err)
	}
	b, _ := os.ReadFile(out)
	var ev alerts.Event
	if err := json.Unmarshal(b, &ev); err != nil || ev.Event != "failed" || !strings.HasPrefix(ev.Summary, "Test: ") {
		t.Errorf("the command got %s (%v)", b, err)
	}
	entries, err := AlertsLog(api.AlertsLogArgs{Lines: 10})
	if err != nil || len(entries) != 2 {
		t.Errorf("log = %+v, %v", entries, err)
	}
	if raw, _ := os.ReadFile(paths.AlertsLog()); strings.Contains(string(raw), "topic-123") || strings.Contains(string(raw), "tk_test") {
		t.Errorf("the log shows a secret: %s", raw)
	}

	if r, err := AlertsOff(); err != nil || !r["removed"] {
		t.Errorf("off = %v, %v", r, err)
	}
	if r, err := AlertsOff(); err != nil || r["removed"] {
		t.Errorf("off again = %v, %v", r, err)
	}
	if info := AlertsGet(); info.Exists {
		t.Error("the file is still there")
	}
}

func asProto(err error, pe **proto.Error) bool { return errors.As(err, pe) }
