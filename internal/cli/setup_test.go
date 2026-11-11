package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
)

func TestAskSetup(t *testing.T) {
	q := client.SetupQuestion{Repo: "/home/me/src/app", Script: "/home/me/src/app/.x/setup", Hash: "h"}
	for in, want := range map[string]bool{"y\n": true, "yes\n": true, "\n": false, "n\n": false, "": false} {
		var out bytes.Buffer
		if got := askSetup(strings.NewReader(in), &out, "box", q, client.SetupNotTrusted); got != want {
			t.Errorf("%q: %v, want %v", in, got, want)
		}
		s := out.String()
		if !strings.Contains(s, "The repository /home/me/src/app on box has a setup script:\n\n    /home/me/src/app/.x/setup") ||
			!strings.Contains(s, "Say yes only for a repository you trust") || !strings.Contains(s, "on box until the script changes") ||
			!strings.HasSuffix(s, "Run it? [y/N] ") {
			t.Fatalf("prompt:\n%s", s)
		}
	}
	if setupValue(true) != api.SetupRun || setupValue(false) != api.SetupSkip {
		t.Fatal("setupValue")
	}
}

// A question says that a script changed only after a yes that kept another
// hash. A yes from before the client kept hashes gets words of its own. So
// does the note without a terminal.
func TestAskSetupAfterAChange(t *testing.T) {
	q := client.SetupQuestion{Repo: "/home/me/src/app", Script: "/home/me/src/app/.x/setup", Hash: "h2"}
	for past, want := range map[client.SetupPast]string{
		client.SetupChanged:   "The setup script of /home/me/src/app on box changed after you trusted it:\n\n    /home/me/src/app/.x/setup\n\n",
		client.SetupUnchecked: "You trusted the setup script of /home/me/src/app on box before this version checked its content:\n\n    /home/me/src/app/.x/setup\n\n",
	} {
		var out bytes.Buffer
		if !askSetup(strings.NewReader("y\n"), &out, "box", q, past) {
			t.Fatal("yes read as no")
		}
		if s := out.String(); !strings.HasPrefix(s, want) {
			t.Fatalf("prompt:\n%s", s)
		}
	}
	for past, want := range map[client.SetupPast]string{
		client.SetupNotTrusted: "Skipping the setup script of /home/me/src/app: you have not said that it may run on box. Pass --setup to run it.",
		client.SetupChanged:    "Skipping the setup script of /home/me/src/app: it changed after you trusted it on box. Pass --setup to run it.",
		client.SetupUnchecked:  "Skipping the setup script of /home/me/src/app: you trusted it on box before this version checked its content. Pass --setup to run it.",
	} {
		if got := skipNote("box", q, past); got != want {
			t.Errorf("past %v: %q, want %q", past, got, want)
		}
	}
}

// When the answers file breaks while a question is open, keeping the answer
// moves it aside, and the warning that names the old file reaches the user.
func TestRememberAnswerShowsTheRecoveryWarning(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if got := client.LoadSetupAnswers("box"); got.Warning != nil {
		t.Fatal(got.Warning)
	}
	trust := filepath.Join(client.StateDir(), "setup-trust.json")
	if err := os.MkdirAll(client.StateDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(trust, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	q := client.SetupQuestion{Repo: "/home/me/src/app", Script: "/home/me/src/app/.x/setup", Hash: "h1"}
	if err := rememberAnswer(&out, "box", q, true); err != nil {
		t.Fatal(err)
	}
	want := "Warning: could not read setup-trust.json, so each setup script asks again. The old file is now setup-trust.json.bad.\n" +
		"Try: Compare the two files, then delete " + trust + ".bad.\n"
	if out.String() != want {
		t.Fatalf("stderr %q, want %q", out.String(), want)
	}
	if got := client.LoadSetupAnswers("box"); got.Hashes[q.Repo] != "h1" {
		t.Errorf("the answer was not kept: %+v", got)
	}
}

// setupHashOf is the setup hash of spec 19.2 for a setup script alone.
func setupHashOf(setup string) string {
	sum := func(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
	return sum("v2\nsetup " + sum(setup) + "\nresume none\nstop none\n")
}

// Without a terminal, new runs a setup script whose content the user
// trusted. It skips one with no answer, and one that changed after the yes,
// with a note that says which.
func TestNewChecksTheSetupScriptsContent(t *testing.T) {
	th := newTestHost(t)
	repo := filepath.Join(th.h.Home, "src", "app")
	script := filepath.Join(repo, "."+brand.Name, "setup")
	write := func(body string) {
		if err := os.MkdirAll(filepath.Dir(script), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	start := func(name string) (id, note string) {
		note = stderrOf(t, func() {
			out := ok(t, "new", "box", "--detach", "--dir", "~/src/app", "--name", name, "--", "/bin/sh", "-c", "echo AGENT-UP; exec sleep 60")
			if m := startedRE.FindStringSubmatch(out); m != nil {
				id = m[1]
			}
		})
		if id == "" {
			t.Fatalf("new %s started nothing: stderr %q", name, note)
		}
		hosttest.WaitFor(t, 30*time.Second, "the program of "+name, func() bool { return strings.Contains(peekOutput(t, id), "AGENT-UP") })
		return id, note
	}
	v1 := "#!/bin/sh\necho SETUP-ONE\n"
	write(v1)
	if err := os.MkdirAll(client.StateDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(client.StateDir(), "setup-trust.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, note := start("unanswered"); !strings.Contains(note, "Warning: could not read setup-trust.json") ||
		!strings.Contains(note, "Skipping the setup script of "+repo+": you have not said that it may run on box.") {
		t.Fatalf("no answer: stderr %q", note)
	}
	if _, err := client.RememberSetup("box", repo, true, setupHashOf(v1)); err != nil {
		t.Fatal(err)
	}
	id, note := start("trusted")
	if strings.Contains(note, "Skipping") || !strings.Contains(peekOutput(t, id), "SETUP-ONE") {
		t.Fatalf("trusted: stderr %q, output %q", note, peekOutput(t, id))
	}
	write("#!/bin/sh\necho SETUP-TWO\n")
	id, note = start("changed")
	if !strings.Contains(note, "Skipping the setup script of "+repo+": it changed after you trusted it on box.") {
		t.Errorf("changed: stderr %q", note)
	}
	if strings.Contains(peekOutput(t, id), "SETUP-TWO") {
		t.Errorf("the changed script ran: %q", peekOutput(t, id))
	}
	// A yes with no hash, as from version 1 of the file, claims no change.
	if _, err := client.RememberSetup("box", repo, true, ""); err != nil {
		t.Fatal(err)
	}
	id, note = start("unchecked")
	if !strings.Contains(note, "Skipping the setup script of "+repo+": you trusted it on box before this version checked its content.") {
		t.Errorf("unchecked: stderr %q", note)
	}
	if strings.Contains(peekOutput(t, id), "SETUP-TWO") {
		t.Errorf("the unchecked script ran: %q", peekOutput(t, id))
	}
}

func TestAskMakeFolder(t *testing.T) {
	q := client.FolderQuestion{Folder: "/home/me/src/new-app"}
	for in, want := range map[string]bool{"y\n": true, "yes\n": true, "\n": false, "n\n": false, "": false} {
		var out bytes.Buffer
		if got := askMakeFolder(strings.NewReader(in), &out, "box", q); got != want {
			t.Errorf("%q: %v, want %v", in, got, want)
		}
		if s := out.String(); s != "The folder /home/me/src/new-app does not exist on box.\nMake it? [y/N] " {
			t.Fatalf("prompt:\n%s", s)
		}
	}
}
