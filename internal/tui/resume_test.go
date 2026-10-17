package tui

import (
	"slices"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

// resumeCalls are the resumes that the launcher asked the hosts for.
func resumeCalls(be *fakeBackend) []string {
	return slices.DeleteFunc(be.callList(), func(c string) bool { return !strings.HasPrefix(c, "resume ") })
}

// After the memory cap stopped an agent, the resume comes first, in the
// line under the list and in the keys. u resumes it in the same session,
// and the launcher then attaches to it, as after a start.
func TestResumeAfterTheCap(t *testing.T) {
	hs := endedLab(t, 80, 24)
	out := plainScreen(hs.m)
	for _, want := range []string{
		"✕ stopped at its memory cap of 1.5 GiB   u resume",
		"u resume   d delete   enter peek   esc hosts   ctrl+o commands",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q on the screen:\n%s", want, out)
		}
	}
	hs.key("u")
	if calls := resumeCalls(hs.be); len(calls) != 1 || calls[0] != "resume gcp-dev g3 memory=0 secrets=map[]" {
		t.Fatalf("resume calls = %q", calls)
	}
	if len(hs.be.attached) != 1 || hs.be.attached[0].Session != "g3" {
		t.Fatalf("after the resume, attached to %+v", hs.be.attached)
	}
	if s := hs.m.selected(); s == nil || s.ID != "g3" || ended(*s) {
		t.Errorf("the importer's row after the resume: %+v", s)
	}
}

// The key needs an agent that has ended. The palette offers the resume
// with its key, and says why it cannot run.
func TestResumeNeedsAnEndedAgent(t *testing.T) {
	hs := sessions(t, 80, 24)
	if s := hs.m.selected(); s == nil || ended(*s) {
		t.Fatalf("selected %+v, want a live session", s)
	}
	hs.key("u")
	hasToast(t, hs.m, "Not available: the agent still runs.")
	if calls := resumeCalls(hs.be); len(calls) != 0 {
		t.Fatalf("resumed a live agent: %q", calls)
	}
	for _, c := range commands {
		if c.name() != "session: resume" {
			continue
		}
		if c.keyLabel() != "u" || c.label() != "Resume session" || c.reason(hs.m) != "the agent still runs" {
			t.Errorf("the palette's resume: key %q, label %q, reason %q", c.keyLabel(), c.label(), c.reason(hs.m))
		}
		hs.key("end")
		if r := c.reason(hs.m); r != "" {
			t.Errorf("the palette cannot resume an agent that exited: %q", r)
		}
		return
	}
	t.Fatal("no resume in the palette")
}

// After a clean exit, as when the user quits the agent, a delete is more
// likely, so it comes first. A lost agent comes back with u too, and the
// restore of every lost session follows. Without a holder, Enter names the
// keys.
func TestResumeStepsFollowHowTheAgentEnded(t *testing.T) {
	hs := sessions(t, 120, 36)
	hs.key("end")
	if out := plainScreen(hs.m); !strings.Contains(out, "✓ exited with code 0   d delete   u resume") {
		t.Errorf("after a clean exit:\n%s", out)
	}
	hs.key("enter")
	if ts := hs.m.toasts.shown; len(ts) == 0 || ts[len(ts)-1].hint != "Press u to resume it, or d to delete it." {
		t.Errorf("Enter without a holder: %+v", ts)
	}

	hs = endedLab(t, 120, 36)
	hs.key("down", "down")
	restore := restoreCommand(hs.m.hostByName("gcp-dev"))
	if out := plainScreen(hs.m); !strings.Contains(out, "◌ the host restarted   u resume   → "+restore+"   d delete") {
		t.Errorf("a lost agent:\n%s", out)
	}
	hs.key("u")
	if calls := resumeCalls(hs.be); len(calls) != 1 || !strings.HasPrefix(calls[0], "resume gcp-dev g5 ") {
		t.Errorf("resume calls = %q", calls)
	}
}

// A resume fetches the secrets the session started with that hosts.toml
// gives the host, and no others. The host names the secrets, so a secret
// that hosts.toml does not give it needs the command line, where the user
// names it too: the launcher fetches nothing, and says so. A failed resume
// says why, with the step to try.
func TestResumeSecretsAndErrors(t *testing.T) {
	hs := endedLab(t, 80, 24)
	h := hs.m.hostByName("gcp-dev")
	setSecrets := func(names ...string) {
		for i := range h.sessions {
			if h.sessions[i].ID == "g3" {
				h.sessions[i].Secrets = names
			}
		}
	}
	h.host.Secrets = []string{"TOKEN"}
	hs.be.secrets = map[string]api.Secret{"TOKEN": "t0k", "OTHER": "x", "GITHUB_TOKEN": "gh"}
	setSecrets("TOKEN", "GITHUB_TOKEN")
	hs.key("u")
	if calls := hs.be.callList(); slices.ContainsFunc(calls, func(c string) bool { return strings.HasPrefix(c, "resume ") || strings.HasPrefix(c, "secrets ") }) {
		t.Fatalf("a host that asked for a secret it is not given got a resume: %q", calls)
	}
	hasToast(t, hs.m, "Importer on gcp-dev asks for secrets that hosts.toml does not give gcp-dev: GITHUB_TOKEN.")
	if ts := hs.m.toasts.shown; !strings.Contains(ts[len(ts)-1].hint, "resume gcp-dev/g3 --secret GITHUB_TOKEN") {
		t.Errorf("the step to pass it: %q", ts[len(ts)-1].hint)
	}

	setSecrets("TOKEN")
	hs.be.state["gcp-dev"].resumeErr = &client.UserError{Msg: "the helper on gcp-dev is too old to resume a session", Hint: "update it"}
	hs.key("u")
	if calls := resumeCalls(hs.be); len(calls) != 1 || calls[0] != "resume gcp-dev g3 memory=0 secrets=map[TOKEN:[redacted]]" {
		t.Fatalf("resume calls = %q", calls)
	}
	hasToast(t, hs.m, "The helper on gcp-dev is too old to resume a session.")
	if len(hs.be.attached) != 0 {
		t.Fatal("attached after a failed resume")
	}
}

// While a resume of the session runs, u does nothing more, and after the
// memory cap, the notice says which cap the agent gets again.
func TestResumeOnceAtATime(t *testing.T) {
	hs := endedLab(t, 80, 24)
	cmd := hs.m.resumeSelected()
	if cmd == nil || !hs.m.resuming[sessKey{"gcp-dev", "g3"}] {
		t.Fatal("u did not start a resume")
	}
	hasToast(t, hs.m, "Resuming importer on gcp-dev, with its cap of 1.5 GiB…")
	if again := hs.m.resumeSelected(); again != nil {
		t.Error("a second u started another resume of the same session")
	}
	hs.do(cmd)
	if hs.m.resuming[sessKey{"gcp-dev", "g3"}] || len(resumeCalls(hs.be)) != 1 {
		t.Errorf("after the resume: running %v, calls %q", hs.m.resuming, resumeCalls(hs.be))
	}
}

// When the selection has moved on by the time the agent runs again, the
// launcher does not attach, and says that it runs.
func TestResumedElsewhere(t *testing.T) {
	hs := endedLab(t, 80, 24)
	res := &client.ResumeResult{Session: client.Session{ID: "g4", Name: "fuzz", State: api.StateWorking, Alive: true}}
	hs.m.attach = &client.AttachState{Key: "gcp-dev/g4", Offset: 4096, Valid: true}
	hs.do(hs.m.onResumed(resumedMsg{key: sessKey{"gcp-dev", "g4"}, res: res}))
	if len(hs.be.attached) != 0 {
		t.Fatal("attached to a session that is no longer selected")
	}
	hasToast(t, hs.m, "Resumed fuzz on gcp-dev.")
	if hs.m.attach.Valid {
		t.Error("the old holder's offset would be replayed from the new one")
	}
}
