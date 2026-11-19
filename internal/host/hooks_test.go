package host

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/agents"
)

// sendSessionStart sends a holder of the agent kind the session-start hook
// with the ID id. The holder had the ID "before", which it chose itself.
func sendSessionStart(t *testing.T, kind, id string) (*Holder, string) {
	t.Helper()
	return sendSessionStartAfter(t, AgentInfo{Kind: kind, AgentSessionID: "before", IDSource: "chosen"}, id)
}

// sendSessionStartAfter sends a holder whose session has the agent record a
// the session-start hook with the ID id. It returns the holder and its log.
func sendSessionStartAfter(t *testing.T, a AgentInfo, id string) (*Holder, string) {
	t.Helper()
	var log bytes.Buffer
	h := &Holder{m: &Manifest{Agent: a}, log: newHolderLog(&log, nil)}
	payload, err := json.Marshal(map[string]string{"session_id": id, "source": "startup"})
	if err != nil {
		t.Fatal(err)
	}
	h.applyHook(HookEvent{Event: "session-start", Payload: payload})
	return h, log.String()
}

// A session-start hook gives the agent's own session ID, which a restore
// or a resume passes to the agent. The holder keeps it only when it has
// the form of the agent's IDs. Any other ID changes nothing, and the
// holder's log says why, on one line.
func TestSessionStartChecksTheID(t *testing.T) {
	const uuid4, uuid7 = "3f2b8c1e-9d4a-4e6b-8f7c-1a2b3c4d5e6f", "0199a3f1-7c2e-7d4b-9a5e-3f1c2b4d5e6f"
	noForm := "does not have the form of the agent's session IDs"
	noTable := "the agent table gives no form for this agent's session IDs"
	for _, c := range []struct {
		kind, id string
		why      string // "" when the holder keeps the ID
	}{
		{"claude", uuid4, ""},
		{"claude", uuid7, ""},
		{"codex", uuid7, ""},
		{"claude", "--dangerously-skip-permissions", noForm},
		{"claude", "--permission-mode=bypassPermissions", noForm},
		{"claude", "--settings", noForm},
		{"claude", uuid4 + "\n--dangerously-skip-permissions", noForm},
		{"claude", uuid4 + " ", noForm},
		{"claude", strings.ToUpper(uuid4), noForm},
		{"claude", "abc-123", noForm},
		{"codex", "--yolo", noForm},
		{"codex", "-c", noForm},
		{"codex", `sandbox_mode="danger-full-access"`, noForm},
		// An agent without a form in the table keeps no ID from a hook.
		{"opencode", uuid4, noTable},
		{"custom", uuid4, noTable},
	} {
		h, log := sendSessionStart(t, c.kind, c.id)
		a := h.m.Agent
		if c.why == "" {
			if a.AgentSessionID != c.id || a.IDSource != "hook" || len(a.IDHistory) != 1 || !h.dirty || !h.hookSeen || log != "" {
				t.Errorf("%s, ID %q: not kept: %+v, log %q", c.kind, c.id, a, log)
			}
			continue
		}
		if a.AgentSessionID != "before" || a.IDSource != "chosen" || len(a.IDHistory) != 0 || h.dirty || h.hookSeen {
			t.Errorf("%s, ID %q: the hook changed the session: %+v, dirty %v, hook seen %v", c.kind, c.id, a, h.dirty, h.hookSeen)
		}
		if !strings.Contains(log, "session ID from a hook not kept") || !strings.Contains(log, c.why) ||
			!strings.Contains(log, "kind="+c.kind) || strings.Count(log, "\n") != 1 {
			t.Errorf("%s, ID %q: log %q, want one line that says %q", c.kind, c.id, log, c.why)
		}
	}
}

// The same valid ID again, as at the agent's start, or no ID, changes no
// ID, adds no history and logs nothing. The hook counts as seen.
func TestSessionStartWithTheSameID(t *testing.T) {
	const uuid = "3f2b8c1e-9d4a-4e6b-8f7c-1a2b3c4d5e6f"
	for _, id := range []string{"", uuid} {
		h, log := sendSessionStartAfter(t, AgentInfo{Kind: "claude", AgentSessionID: uuid, IDSource: "chosen"}, id)
		if a := h.m.Agent; a.AgentSessionID != uuid || a.IDSource != "chosen" || len(a.IDHistory) != 0 || !h.hookSeen || log != "" {
			t.Errorf("ID %q: %+v, hook seen %v, log %q", id, a, h.hookSeen, log)
		}
	}
}

// An older helper kept any ID from a hook. The same invalid ID again is
// checked like a new one: it changes nothing, and the log says why.
func TestSessionStartWithTheSameInvalidID(t *testing.T) {
	const bad = "--dangerously-skip-permissions"
	old := AgentInfo{Kind: "claude", AgentSessionID: bad, IDSource: "hook", IDHistory: []IDEvent{{ID: bad}}}
	h, log := sendSessionStartAfter(t, old, bad)
	if a := h.m.Agent; a.AgentSessionID != bad || a.IDSource != "hook" || len(a.IDHistory) != 1 || h.hookSeen || h.dirty {
		t.Errorf("the hook changed the session: %+v, hook seen %v, dirty %v", a, h.hookSeen, h.dirty)
	}
	if !strings.Contains(log, "session ID from a hook not kept") || !strings.Contains(log, "does not have the form") {
		t.Errorf("log %q does not say why", log)
	}
}

// FuzzApplyHook sends any event, with any payload, to a holder of each kind
// of agent. The holder never fails, and the ID that it keeps for a resume
// has the agent's form. The resume command that the holder's record makes
// holds no argument that turns the agent's approvals off.
func FuzzApplyHook(f *testing.F) {
	f.Add("session-start", `{"session_id":"3f2b8c1e-9d4a-4e6b-8f7c-1a2b3c4d5e6f","source":"startup"}`)
	f.Add("session-start", `{"session_id":"--dangerously-skip-permissions","source":"startup"}`)
	f.Add("session-start", `{"session_id":"3f2b8c1e-9d4a-4e6b-8f7c-1a2b3c4d5e6f --yolo"}`)
	f.Add("session-start", `{"session_id":"-c"}`)
	f.Add("session-start", `{"session_id":123}`)
	f.Add("session-start", `not json`)
	f.Add("notification", `{"message":"Claude needs your permission"}`)
	f.Add("codex-notify", `{"type":"agent-turn-complete","thread-id":"t1"}`)
	f.Add("prompt", ``)
	f.Add("stop", `{}`)
	f.Fuzz(func(t *testing.T, event, payload string) {
		for _, kind := range []string{"claude", "codex", "opencode", "custom"} {
			agent, _ := agents.Find(kind)
			h := &Holder{m: &Manifest{Agent: AgentInfo{
				Kind: kind, ResumeArgv: agent.Resume, ContinueArgv: agent.Continue,
				Hooks: hookArgs(agent.Hooks), Inline: agent.Inline.Args,
			}}, log: slog.New(slog.DiscardHandler)}
			h.applyHook(HookEvent{Event: event, Payload: json.RawMessage(payload)})
			id := h.m.Agent.AgentSessionID
			if id != "" && !agent.ValidID(id) {
				t.Fatalf("%s kept the ID %q from %s %q", kind, id, event, payload)
			}
			argv, _, err := resumeArgv(h.m)
			if err != nil {
				t.Fatalf("%s after %s %q: %v", kind, event, payload, err)
			}
			for _, s := range argv {
				if id != "" && strings.HasPrefix(s, "-") && strings.Contains(s, id) {
					t.Fatalf("%s: the ID %q made the argument %q", kind, id, s)
				}
			}
		}
	})
}
