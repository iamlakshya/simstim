package alerts

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
)

func TestSummary(t *testing.T) {
	code, zero := 2, 0
	s := Session{ID: "abc", Name: "api-tests"}
	cases := []struct {
		ev   Event
		want string
	}{
		{Event{Event: NeedsInput, Session: Session{ID: "abc", Name: "auth-fix"}}, "auth-fix on box needs you"},
		{Event{Event: Failed, Session: s, ExitCode: &code}, "api-tests on box failed (exit 2)"},
		{Event{Event: Failed, Session: s, ExitCode: &code, Signal: "killed"}, "api-tests on box failed (signal: killed)"},
		{Event{Event: Exited, Session: s, ExitCode: &zero}, "api-tests on box ended"},
		{Event{Event: TurnDone, Session: s, Turn: 4*time.Minute + 12*time.Second}, "api-tests on box finished a turn (4m12s)"},
		{Event{Event: TurnDone, Session: s, Turn: 2 * time.Hour}, "api-tests on box finished a turn (2h)"},
		{Event{Event: OOM, Session: s}, "api-tests on box ran out of memory, and a process was stopped"},
		{Event{Event: Started, Session: s}, "api-tests started on box"},
		{Event{Event: Restored, Session: s}, "api-tests was restored on box after a restart"},
		{Event{Event: Test}, "Test alert from box"},
		{Event{Event: NeedsInput, Session: Session{ID: "abc"}}, "abc on box needs you"},
		{Event{Event: Memory, Session: s, Usage: "2.7 of 2.9 GiB"}, "api-tests on box is close to its memory cap (2.7 of 2.9 GiB)"},
		{Event{Event: Memory, Session: s}, "api-tests on box is close to its memory cap"},
		{Event{Event: ModelBudget, Session: s, Usage: "2000412 of 2000000 tokens"}, "api-tests on box reached its model token budget (2000412 of 2000000 tokens)"},
		{Event{Event: ModelBudget, Session: s}, "api-tests on box reached its model token budget"},
	}
	for _, c := range cases {
		c.ev.Host = "box"
		if got := summary(c.ev); got != c.want {
			t.Errorf("summary(%s) = %q, want %q", c.ev.Event, got, c.want)
		}
	}
}

func TestEventJSON(t *testing.T) {
	ev := prepare(Event{Event: NeedsInput, Session: Session{ID: "abc", Name: "n", Agent: "claude", Dir: "/d"},
		Detail: "secret code", at: time.Date(2026, 10, 15, 10, 0, 0, 123, time.UTC)}, "box", &Settings{})
	var m map[string]any
	if err := json.Unmarshal(ev.JSON(), &m); err != nil {
		t.Fatal(err)
	}
	if m["v"] != 1.0 || m["event"] != "needs_input" || m["host"] != "box" || m["time"] != "2026-10-15T10:00:00Z" || m["summary"] != "n on box needs you" {
		t.Errorf("json = %v", m)
	}
	for _, k := range []string{"exit_code", "signal", "detail", "Turn"} {
		if _, ok := m[k]; ok {
			t.Errorf("json has %s: %v", k, m)
		}
	}
	sess, _ := m["session"].(map[string]any)
	if sess["id"] != "abc" || sess["name"] != "n" || sess["agent"] != "claude" || sess["dir"] != "/d" {
		t.Errorf("session = %v", sess)
	}
}

func TestEventEnv(t *testing.T) {
	code := 3
	ev := Event{V: 1, Event: Failed, Host: "box", Session: Session{ID: "abc", Name: "a\x00b"}, ExitCode: &code, Summary: "s"}
	env := strings.Join(ev.env(), "\n")
	for _, want := range []string{brand.Env("EVENT") + "=failed", brand.Env("SESSION_NAME") + "=ab", brand.Env("EXIT_CODE") + "=3", brand.Env("HOST") + "=box"} {
		if !strings.Contains(env, want) {
			t.Errorf("env lacks %q:\n%s", want, env)
		}
	}
}

// An unwalled event carries no detail_from field or <NAME>_DETAIL_FROM variable;
// a walled event marks its guest-originated detail with detail_from="guest"
// (docs/contributing/isolation-spec.md 10.8).
func TestEventDetailFrom(t *testing.T) {
	ev := Event{V: 1, Event: NeedsInput, Host: "box", Detail: "Proceed?", Summary: "s"}
	if j := string(ev.JSON()); strings.Contains(j, "detail_from") {
		t.Errorf("unwalled JSON has detail_from: %s", j)
	}
	if env := strings.Join(ev.env(), "\n"); strings.Contains(env, "DETAIL_FROM") {
		t.Errorf("unwalled env has DETAIL_FROM:\n%s", env)
	}

	walled := Event{V: 1, Event: NeedsInput, Host: "box", Detail: "Proceed?", DetailFrom: "guest", Summary: "s"}
	if j := string(walled.JSON()); !strings.Contains(j, `"detail_from":"guest"`) {
		t.Errorf("walled JSON lacks detail_from=guest: %s", j)
	}
	if env := strings.Join(walled.env(), "\n"); !strings.Contains(env, brand.Env("DETAIL_FROM")+"=guest") {
		t.Errorf("walled env lacks DETAIL_FROM=guest:\n%s", env)
	}
}
