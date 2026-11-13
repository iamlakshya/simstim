package client

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// seedTrustSafety keeps both kinds of answer so a failed save cannot silently
// discard a refusal or a content-bound yes. The tests use a state of their own.
func seedTrustSafety(t *testing.T) []byte {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	for _, answer := range []struct {
		repo string
		run  bool
		hash string
	}{{"/refused", false, ""}, {"/trusted", true, "kept-hash"}} {
		if warn, err := RememberSetup("box", answer.repo, answer.run, answer.hash); warn != nil || err != nil {
			t.Fatalf("seed answers: %v, %v", warn, err)
		}
	}
	return trustSafetyRead(t, trustPath())
}

func trustSafetyRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func trustSafetyEntries(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(StateDir())
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.Name()
	}
	return names
}

func assertTrustSafetyEntries(t *testing.T, want []string) {
	t.Helper()
	if got := trustSafetyEntries(t); !slices.Equal(got, want) {
		t.Errorf("save left or removed directory entries: got %q, want %q", got, want)
	}
}

// cancelWrittenTrustTempContext cancels only when a new file contains bytes.
// It observes this operation's directory rather than counting Err calls or
// replacing a global filesystem hook. Existing files, including a foreign
// legacy temporary file, are never treated as this operation's output.
type cancelWrittenTrustTempContext struct {
	context.Context
	cancel   context.CancelFunc
	dir      string
	known    map[string]bool
	observed string
}

func (c *cancelWrittenTrustTempContext) Err() error {
	if err := c.Context.Err(); err != nil {
		return err
	}
	entries, err := os.ReadDir(c.dir)
	if err == nil {
		for _, entry := range entries {
			if c.known[entry.Name()] || entry.IsDir() {
				continue
			}
			if b, err := os.ReadFile(filepath.Join(c.dir, entry.Name())); err == nil && len(b) > 0 {
				c.observed = entry.Name()
				c.cancel()
				break
			}
		}
	}
	return c.Context.Err()
}

// testForeignTrustTemp exercises the same ownership contract for a regular
// legacy .tmp file and, on Unix, a symlink and the separate file it names.
func testForeignTrustTemp(t *testing.T, foreign func(*testing.T, string) func()) {
	t.Helper()
	for _, outcome := range []string{"success", "cancel-before-rename", "rename-failure"} {
		t.Run(outcome, func(t *testing.T) {
			before := seedTrustSafety(t)
			checkForeign := foreign(t, trustPath()+".tmp")
			// The direct persistence tests honour the same process and file
			// ownership as RememberSetupContext. No other writer can race them.
			trustMu.Lock()
			defer trustMu.Unlock()
			unlock, err := lockTrust()
			if err != nil {
				t.Fatal(err)
			}
			defer unlock()
			file, warn, stuck := loadTrust()
			if warn != nil || stuck {
				t.Fatalf("load fixture: %v, %v", warn, stuck)
			}
			var child string
			if outcome == "rename-failure" {
				if err := os.Remove(trustPath()); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(trustPath(), 0o700); err != nil {
					t.Fatal(err)
				}
				child = filepath.Join(trustPath(), "sentinel")
				if err := os.WriteFile(child, []byte("keep the directory child"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			names := trustSafetyEntries(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			observing := &cancelWrittenTrustTempContext{Context: ctx, cancel: cancel, dir: StateDir(), known: map[string]bool{}}
			for _, name := range names {
				observing.known[name] = true
			}
			var saveCtx context.Context = ctx
			if outcome == "cancel-before-rename" {
				saveCtx = observing
			}
			err = saveAnswerContext(saveCtx, file, "box", "/new", true, "new-hash")
			switch outcome {
			case "success":
				if err != nil {
					t.Errorf("save: %v", err)
				}
				var saved trustFile
				if data := trustSafetyRead(t, trustPath()); json.Unmarshal(data, &saved) != nil {
					t.Errorf("save did not leave valid trust JSON: %s", data)
				} else {
					refused, found := saved.Hosts["box"]["/refused"]
					if !found || refused || saved.Trusted["box"]["/trusted"].SHA256 != "kept-hash" || saved.Trusted["box"]["/new"].SHA256 != "new-hash" {
						t.Errorf("save lost or changed the answers: %+v", saved)
					}
				}
			case "cancel-before-rename":
				if observing.observed == "" || !errors.Is(err, context.Canceled) {
					t.Errorf("save did not stop after writing its own temporary file: observed %q, error %v", observing.observed, err)
				}
				if got := trustSafetyRead(t, trustPath()); string(got) != string(before) {
					t.Errorf("cancel changed the committed answers: %q", got)
				}
			case "rename-failure":
				if err == nil || errors.Is(err, context.Canceled) {
					t.Errorf("rename over a nonempty directory = %v, want a filesystem error", err)
				}
				if got := trustSafetyRead(t, child); string(got) != "keep the directory child" {
					t.Errorf("failed rename changed the directory child: %q", got)
				}
			}
			checkForeign()
			assertTrustSafetyEntries(t, names)
		})
	}
}

func TestSetupTrustPreservesForeignRegularTemp(t *testing.T) {
	testForeignTrustTemp(t, func(t *testing.T, path string) func() {
		t.Helper()
		const want = "a different writer owns this temporary file"
		if err := os.WriteFile(path, []byte(want), 0o600); err != nil {
			t.Fatal(err)
		}
		before, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		return func() {
			t.Helper()
			after, err := os.Lstat(path)
			if err != nil || !os.SameFile(before, after) {
				t.Errorf("save removed or replaced the foreign temporary file: %v", err)
				return
			}
			if got := trustSafetyRead(t, path); string(got) != want {
				t.Errorf("save changed the foreign temporary file: %q", got)
			}
		}
	})
}

func assertTrustReadFailure(t *testing.T, err error) {
	t.Helper()
	var ue *UserError
	if !errors.As(err, &ue) || ue == nil {
		t.Errorf("unreadable answers = %v, want a trust_file error", err)
		return
	}
	if ue.Code != "trust_file" || strings.TrimSpace(ue.Hint) == "" || ue.Kind == KindCancelled || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("unreadable answers have the wrong code, hint or kind: %+v", ue)
	}
	for _, falseClaim := range []string{"old file is now", "move it aside", "cancelled"} {
		if strings.Contains(strings.ToLower(ue.Msg), falseClaim) {
			t.Errorf("unreadable answers claim %q: %s", falseClaim, ue.Msg)
		}
	}
}

// Reading a directory as a file fails even for a privileged user and on
// Windows. This always exercises fail-closed behaviour when Unix mode bits
// cannot make a regular file unreadable.
func TestSetupTrustFailsClosedWhenTheAnswersCannotBeRead(t *testing.T) {
	seedTrustSafety(t)
	if err := os.Remove(trustPath()); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(trustPath(), 0o700); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(trustPath(), "sentinel")
	const want = "preserve the existing directory"
	if err := os.WriteFile(child, []byte(want), 0o600); err != nil {
		t.Fatal(err)
	}
	names := trustSafetyEntries(t)
	answers := LoadSetupAnswers("box")
	if len(answers.Refused)+len(answers.Hashes)+len(answers.Trusted) != 0 {
		t.Errorf("an unreadable file trusted answers: %+v", answers)
	}
	assertTrustReadFailure(t, answers.Warning)
	for _, op := range []struct {
		name     string
		remember func() (*UserError, error)
	}{
		{"context", func() (*UserError, error) {
			return RememberSetupContext(context.Background(), "box", "/new", true, "new-hash")
		}},
		{"legacy", func() (*UserError, error) { return RememberSetup("box", "/new", true, "new-hash") }},
	} {
		warn, err := op.remember()
		if warn != nil {
			t.Errorf("%s returned a recovery warning for an unreadable file: %v", op.name, warn)
		}
		assertTrustReadFailure(t, err)
	}
	if got := trustSafetyRead(t, child); string(got) != want {
		t.Errorf("read failure changed the existing directory: %q", got)
	}
	assertTrustSafetyEntries(t, names)
}
