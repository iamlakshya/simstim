//go:build unix

package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/client/helpers"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
)

// The second acceptance line of C4, updated for H4's tier question: with
// --no-input, `host add plain` asks for --tier first, and with --tier set it
// fails with exit code 4 and the flag --yes. Input off also means that SSH
// asks nothing (BatchMode=yes), and that its own words stay off stderr.
func TestHostAddNoInputCommand(t *testing.T) {
	if _, ok := helpers.For("linux", "arm64"); !ok {
		t.Skip("this build has no helper for linux/arm64 (run just helpers)")
	}
	h := hosttest.New(t)
	h.Script("ssh", promptingSSH)
	hostShows(t, h.Home, "trusted") // the user's settings trust the host key: no question
	t.Setenv("PATH", filepath.Join(h.Root, "bin")+":"+os.Getenv("PATH"))
	out, errOut, code := mainRun(t, "host", "add", "plain", "--no-input")
	if code != 4 || out != "" || !strings.Contains(errOut, "Error: host add needs the tier of plain (own, throwaway or shared), and input is off.") ||
		!strings.Contains(errOut, "Try: "+brand.Name+" host add plain --no-input --tier <tier>") {
		t.Fatalf("host add --no-input: exit %d\nstdout %q\nstderr %s", code, out, errOut)
	}
	out, errOut, code = mainRun(t, "host", "add", "plain", "--tier", "own", "--no-input")
	if code != 4 || out != "" || !strings.Contains(errOut, "Error: host add needs a yes to install the helper on plain, and input is off.") ||
		!strings.Contains(errOut, "Try: "+brand.Name+" host add plain --tier own --no-input --yes") {
		t.Fatalf("host add --tier own --no-input: exit %d\nstdout %q\nstderr %s", code, out, errOut)
	}
	if strings.Contains(errOut, "ssh-noise") || strings.Contains(errOut, "Password") {
		t.Errorf("ssh asked, or its words reached stderr:\n%s", errOut)
	}
	if _, err := client.FindHost("plain"); err == nil {
		t.Error("the host was saved")
	}
	if client.HasPin("plain") {
		t.Error("a host add that failed left a pin") // isolation spec 14.7.1
	}
	// Every ssh ran with BatchMode=yes, so none could ask: a test of the
	// time it took would fail on a busy machine.
	log, _ := os.ReadFile(filepath.Join(h.Home, "ssh.log"))
	if len(log) == 0 || strings.Contains(string(log), "base64") {
		t.Errorf("ssh ran %q", log)
	}
	for _, l := range strings.Split(strings.TrimSpace(string(log)), "\n") {
		if !strings.Contains(" "+l+" ", " BatchMode=yes ") {
			t.Errorf("ssh ran without BatchMode=yes: %q", l)
		}
	}

	// With --json too, stderr gets only JSON lines.
	if err := client.SaveHostsContext(context.Background(), []client.Host{{Name: "plain", SSH: "plain"}}); err != nil {
		t.Fatal(err)
	}
	pinFor(t, "plain") // no note of a new pin
	out, errOut, code = mainRun(t, "host", "test", "plain", "--json")
	if code != 1 || out != "" || strings.Count(errOut, "\n") != 1 || !json.Valid([]byte(errOut)) || strings.Contains(errOut, "Password") {
		t.Errorf("host test --json: exit %d\nstdout %q\nstderr %q", code, out, errOut)
	}
}

// agents install with input off starts the installer without the
// terminal, and says how to attach to answer it.
func TestAgentsInstallNoInput(t *testing.T) {
	withFake(t, nil)
	if err := saveHostContext(context.Background(), client.Host{Name: "box", Machine: &client.Machine{Provider: "fake", ID: "fake-box"}}); err != nil {
		t.Fatal(err)
	}
	term, done := inTerminal(t, "agents", "install", "claude", "--host", "box", "--no-input")
	if err := wait(t, done); err != nil {
		t.Fatalf("agents install --no-input: %v\n%s", err, term.Output())
	}
	term.WaitFor("Attach to answer its questions: " + brand.Name + " attach box/fake2")
}
