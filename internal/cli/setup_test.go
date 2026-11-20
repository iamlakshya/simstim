package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/proto"
)

// testPrompts is a prompter with input on, which reads in and writes to out.
func testPrompts(in string, out *bytes.Buffer) *prompter {
	return &prompter{ctx: context.Background(), in: strings.NewReader(in), out: out, on: true, line: []string{"new", "box"}, flagsAt: 2}
}

func TestAskSetup(t *testing.T) {
	q := client.SetupQuestion{Repo: "/home/me/src/app", Script: "/home/me/src/app/.x/setup", Hash: "h"}
	for in, want := range map[string]bool{"y\n": true, "yes\n": true, "\n": false, "n\n": false} {
		var out bytes.Buffer
		if got, err := askSetup(testPrompts(in, &out), "box", q, client.SetupNotTrusted); got != want || err != nil {
			t.Errorf("%q: %v %v, want %v", in, got, err, want)
		}
		s := out.String()
		if !strings.Contains(s, "The repository /home/me/src/app on box has a setup script:\n\n    /home/me/src/app/.x/setup") ||
			!strings.Contains(s, "Say yes only for a repository you trust") || !strings.Contains(s, "on box until the script changes") ||
			!strings.HasSuffix(s, "Run it? [y/N] ") {
			t.Fatalf("prompt:\n%s", s)
		}
	}
	// The end of input cancels.
	var out bytes.Buffer
	if _, err := askSetup(testPrompts("", &out), "box", q, client.SetupNotTrusted); !isCancel(err) {
		t.Errorf("no answer: %v, want a cancel", err)
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
		if yes, err := askSetup(testPrompts("y\n", &out), "box", q, past); !yes || err != nil {
			t.Fatalf("yes read as %v, %v", yes, err)
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
	if err := rememberAnswer(context.Background(), &out, "box", q, true); err != nil {
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
		out, warnings, err := runApart(t, "new", "box", "--detach", "--json", "--dir", "~/src/app", "--name", name, "--", "/bin/sh", "-c", "echo AGENT-UP; exec sleep 60")
		if err != nil {
			t.Fatalf("new %s: %v: %s", name, err, warnings)
		}
		var session client.Session
		if err := json.Unmarshal([]byte(out), &session); err != nil {
			t.Fatal(err)
		}
		id = session.ID
		// Decode warning messages, while retaining their original text checks.
		for _, line := range strings.Split(strings.TrimSpace(warnings), "\n") {
			if line == "" {
				continue
			}
			var item struct{ Warning struct{ Message string } }
			if err := json.Unmarshal([]byte(line), &item); err != nil {
				t.Fatal(err)
			}
			note += item.Warning.Message + "\n"
		}
		if id == "" {
			t.Fatalf("new %s started nothing: %s", name, warnings)
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
	for in, want := range map[string]bool{"y\n": true, "yes\n": true, "\n": false, "n\n": false} {
		var out bytes.Buffer
		if got, err := askMakeFolder(testPrompts(in, &out), "box", q); got != want || err != nil {
			t.Errorf("%q: %v %v, want %v", in, got, err, want)
		}
		if s := out.String(); s != "The folder /home/me/src/new-app does not exist on box.\nMake it? [y/N] " {
			t.Fatalf("prompt:\n%s", s)
		}
	}
	// With input off, the question fails with exit code 4, and names --mkdir.
	var out bytes.Buffer
	p := testPrompts("", &out)
	p.on = false
	_, err := askMakeFolder(p, "box", q)
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Kind != client.KindNeedsAnswer || ue.Hint != brand.Name+" new box --mkdir" ||
		ue.Msg != "new needs a yes to make the folder /home/me/src/new-app, which does not exist on box, and input is off" || out.Len() != 0 {
		t.Errorf("input off: %+v, printed %q", err, out.String())
	}
}

// The answer no to a question cancels: the error is the cancel of the
// prompts (cli-spec 6.2, rule 6), with the code of a cancel, and exits with
// 130. The host makes no folder.
func TestAnsweredNoIsACancel(t *testing.T) {
	hosttest.New(t)
	withSignal(t)
	_, tr := withFake(t, nil)
	if err := client.SaveHosts(context.Background(), []client.Host{{Name: "box", Machine: &client.Machine{Provider: "fake", ID: "fake-box"}}}); err != nil {
		t.Fatal(err)
	}
	q, _ := json.Marshal(api.FolderQuestion{Folder: "/x"})
	tr.Fail("new", &proto.Error{Code: api.CodeNoFolder, Message: "the folder /x does not exist on this host", Hint: "Pass --mkdir.", Data: q})
	h, err := client.FindHost("box")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := client.Dial(context.Background(), h, true)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	t.Setenv(noInputEnv, "")
	term := hosttest.NewTerminal(t, 100, 30)
	oldIn, oldOut, oldErr := os.Stdin, os.Stdout, os.Stderr
	os.Stdin, os.Stdout, os.Stderr = term.TTY, term.TTY, term.TTY
	defer func() { os.Stdin, os.Stdout, os.Stderr = oldIn, oldOut, oldErr }()
	term.Type("n\n")
	_, err = startSession(sessionCmd(context.Background()), conn, h, client.NewArgs{})
	if !isCancel(err) || err.Error() != "Cancelled. Nothing changed." || printError(io.Discard, err, nil, false, false) != 130 {
		t.Fatalf("the answer no = %v", err)
	}
	var b bytes.Buffer
	printErrorJSON(&b, err, nil, false)
	if !strings.Contains(b.String(), `"code":"cancelled"`) || !strings.Contains(b.String(), `"kind":"cancelled"`) {
		t.Errorf("the answer no in JSON: %s", b.String())
	}
	term.WaitFor("Make it? [y/N] ")
}
