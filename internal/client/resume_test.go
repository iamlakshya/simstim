package client

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// A helper without the resume feature is too old, and so is one that
// does not know the request: the step is to update it.
func TestResumeNeedsANewHelper(t *testing.T) {
	c := &Conn{Host: Host{Name: "old"}, Features: []string{"mkdir"}}
	_, err := c.Resume(context.Background(), ResumeArgs{ID: "a1"})
	var ue *UserError
	if !errors.As(err, &ue) || ue.Msg != "the helper on old is too old to resume a session" || !strings.Contains(ue.Hint, "host add old") {
		t.Fatalf("resume on an old helper = %v", err)
	}
	unknown := &UserError{Msg: "unknown request resume", Hint: "Update the host helper.", Code: "unknown_op"}
	if err := c.resumeError(unknown); !errors.As(err, &ue) || !strings.Contains(ue.Msg, "too old to resume") {
		t.Errorf("an unknown request = %v, want the helper to be too old", err)
	}
	other := &UserError{Msg: "the agent of app still runs", Code: "agent_running"}
	if err := c.resumeError(other); !errors.Is(err, other) {
		t.Errorf("another error became %v", err)
	}
}

// A resume fetches without asking only the secrets that hosts.toml gives
// the host. The host's manifest names the others, so they need the user's
// --secret.
func TestResumeSecrets(t *testing.T) {
	h := Host{Name: "box", Secrets: []string{"ANTHROPIC_API_KEY"}}
	s := Session{ID: "a1", Name: "app", Secrets: []string{"ANTHROPIC_API_KEY", "GITHUB_TOKEN", "NPM_TOKEN"}}
	_, err := ResumeSecrets(h, s, []string{"NPM_TOKEN"})
	var ue *UserError
	if !errors.As(err, &ue) || !strings.HasSuffix(ue.Msg, ": GITHUB_TOKEN") || !strings.Contains(ue.Hint, "resume box/a1 --secret GITHUB_TOKEN") {
		t.Fatalf("a secret the host is not given = %v", err)
	}
	names, err := ResumeSecrets(h, s, []string{"GITHUB_TOKEN", "NPM_TOKEN", "OTHER"})
	if err != nil || strings.Join(names, " ") != "ANTHROPIC_API_KEY GITHUB_TOKEN NPM_TOKEN" {
		t.Fatalf("names = %q, %v", names, err)
	}
	// A name from the host that no secret could have is refused, and the
	// error shows it quoted, without its escapes.
	s.Secrets = []string{"A\x1b]0;owned\x07; rm -rf ~"}
	_, err = ResumeSecrets(h, s, s.Secrets)
	if !errors.As(err, &ue) || strings.ContainsRune(ue.Msg+ue.Hint, 0x1b) || !strings.Contains(ue.Msg, `\x1b`) {
		t.Fatalf("a name with escapes = %v", err)
	}
}
