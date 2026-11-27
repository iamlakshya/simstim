//go:build unix

package cli

import (
	"bytes"
	"context"
	"encoding/json"
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

// The answer no to a question cancels: the error is the cancel of the
// prompts (cli-spec 6.2, rule 6), with the code of a cancel, and exits with
// 130. The host makes no folder.

func TestAnsweredNoIsACancel(t *testing.T) {
	hosttest.New(t)
	withSignal(t)
	_, tr := withFake(t, nil)
	if err := client.SaveHostsContext(context.Background(), []client.Host{{Name: "box", Machine: &client.Machine{Provider: "fake", ID: "fake-box"}}}); err != nil {
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
