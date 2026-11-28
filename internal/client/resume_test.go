package client

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
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
	if want := "Delete the session with " + brand.Name + " rm box/a1, then start a new one."; ue.Hint != want {
		t.Errorf("hint = %q, want %q", ue.Hint, want)
	}

	// Hostile session IDs, host names or session names never become a
	// runnable command targeting another session after cleaning.
	wantLs := brand.Name + " ls box (lists sessions to check the ID)"
	for _, badID := range []string{"abcd\u202e2345", "ab\x1bc23", "ab cd", "ab/cd", "-flag", "", strings.Repeat("a", 65)} {
		for _, secs := range [][]string{{"GITHUB_TOKEN"}, {"BAD;SECRET"}} {
			bad := Session{ID: badID, Name: "app\x1bc\u202e", Secrets: secs}
			_, err := ResumeSecrets(h, bad, nil)
			if !errors.As(err, &ue) {
				t.Fatalf("bad ID %q (%v): %v", badID, secs, err)
			}
			if strings.ContainsAny(ue.Msg+ue.Hint, "\x1b\u202e") {
				t.Errorf("bad ID %q leaked control/bidi runes: msg=%q hint=%q", badID, ue.Msg, ue.Hint)
			}
			if strings.Contains(ue.Hint, "abcd2345") || ue.Hint != wantLs {
				t.Errorf("bad ID %q (%v): hint = %q, want %q", badID, secs, ue.Hint, wantLs)
			}
		}
	}

	// An oversized missing-secrets hint falls back to ls instead of truncating.
	var many []string
	for i := range 250 {
		many = append(many, "EXTRA_SECRET_TOKEN_"+strings.Repeat("X", 10)+string(rune('A'+i%26)))
	}
	_, err = ResumeSecrets(h, Session{ID: "abcd2345", Name: "app", Secrets: many}, nil)
	if !errors.As(err, &ue) || ue.Hint != wantLs {
		t.Errorf("oversized hint = %q, want %q (err=%v)", ue.Hint, wantLs, err)
	}
	// An invalid host name falls back to bare ls.
	_, err = ResumeSecrets(Host{Name: "bad host\u202e"}, Session{ID: "abcd2345", Name: "app", Secrets: []string{"GITHUB_TOKEN"}}, nil)
	if !errors.As(err, &ue) || ue.Hint != brand.Name+" ls (lists sessions to check the ID)" {
		t.Errorf("invalid host hint = %q (err=%v)", ue.Hint, err)
	}
}
