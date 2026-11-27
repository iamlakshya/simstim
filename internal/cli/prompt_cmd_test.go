package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/client/helpers"
	"github.com/iamlakshya/simstim/internal/onboard"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

// mainRun runs Main with args, and returns its stdout, its stderr and its
// exit code.
func mainRun(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	old := os.Args
	defer func() { os.Args = old }()
	os.Args = append([]string{"client"}, args...)
	var code int
	out, errOut := captureOutput(t, func() { code = Main() })
	return out, errOut, code
}

// promptingSSH is an ssh that would ask for a password without
// BatchMode=yes, and writes a line of its own to stderr. It answers the
// probe as a Linux host without the helper, shows a host key that the
// user's settings trust (keyFake), and reaches nothing else.
const promptingSSH = `case " $* " in *" -G "*) exit 0;; esac
echo "$@" >> "$HOME/ssh.log"
echo "ssh-noise: a banner from ssh" >&2
case " $* " in *" BatchMode=yes "*) ;; *) echo "Password: " >&2; sleep 5;; esac
for a; do last=$a; done
case "$last" in *"uname -sm"*) echo "Linux aarch64"; exit 0;; esac
` + keyFake + `exit 255
`

// host rm --destroy needs --confirm with input off, and so does its old
// name, host destroy, which warns and names the new form.
func TestHostDestroyNoInputCommand(t *testing.T) {
	withFake(t, nil)
	if err := saveHostContext(context.Background(), client.Host{Name: "box", Machine: &client.Machine{Provider: "fake", ID: "fake-box"}}); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"host", "rm", "box", "--destroy", "--no-input"}, {"host", "destroy", "box", "--no-input"}} {
		out, got, code := mainRun(t, args...)
		if code != 4 || out != "" || !strings.Contains(got, "Try: "+brand.Name+" host rm box --destroy --confirm box") {
			t.Errorf("%s: exit %d\n%s", strings.Join(args, " "), code, got)
		}
		if old := args[1] == "destroy"; old != strings.Contains(got, `Warning: "host destroy <host>" is now "host rm <host> --destroy"`) {
			t.Errorf("%s: the warning of the old name:\n%s", strings.Join(args, " "), got)
		}
	}
	if _, err := client.FindHost("box"); err != nil {
		t.Error("the host was forgotten")
	}
}

// With input off, host add takes the checklist's default: it offers no
// agent, and says how to install each. A cancel at the checklist says what
// host add changed before it.
func TestHostAddChecklistRow(t *testing.T) {
	_, tr := withFake(t, nil)
	tr.SetAgents([]api.AgentCheck{{Kind: "claude", Title: "Claude Code", Support: "first-class"}})
	box := client.Host{Name: "box", Machine: &client.Machine{Provider: "fake", ID: "fake-box"}}
	errOut, err := said(t, func() error { return addHost(context.Background(), quietPrompts(context.Background()), box, false) })
	if err != nil || strings.Contains(errOut, "Type the numbers") || !strings.Contains(errOut, "box is ready.") {
		t.Fatalf("input off: %v\n%s", err, errOut)
	}

	var out bytes.Buffer
	_, err = said(t, func() error { return addHost(context.Background(), testPrompts("", &out), box, false) })
	if !isCancel(err) || err.Error() != "Cancelled. Done before that: saved the host box." {
		t.Fatalf("a cancel at the checklist: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "Type the numbers to install") {
		t.Errorf("the checklist did not ask:\n%s", out.String())
	}
}

// host add records each change as it makes it: a cancel after the
// helper's install, before the checklist, says that the helper was
// installed.
func TestHostAddCancelAfterTheInstall(t *testing.T) {
	if _, ok := helpers.For("linux", "arm64"); !ok {
		t.Cleanup(helpers.HideEmbedded())
		cacheDir := filepath.Join(t.TempDir(), brand.Name)
		helperDir := filepath.Join(cacheDir, "helpers", version.Version)
		if err := os.MkdirAll(helperDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(helperDir, "host-linux-arm64"), []byte("fake-helper"), 0o600); err != nil {
			t.Fatal(err)
		}
		if !helpers.Cached(version.Version, cacheDir) {
			t.Fatal("failed to load cached test helper")
		}
	}
	_, tr := withFake(t, nil)
	tr.NoHelper = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tr.OnRun = func(remote string) {
		if strings.Contains(remote, " install-payload") { // the install's last step
			cancel()
		}
	}
	box := client.Host{Name: "box", Machine: &client.Machine{Provider: "fake", ID: "fake-box"}}
	errOut, err := said(t, func() error { return addHost(ctx, quietPrompts(ctx), box, true) })
	if !isCancel(err) || err.Error() != "Cancelled. Done before that: installed the helper on box." {
		t.Fatalf("a cancel after the install: %v\n%s", err, errOut)
	}
	if !strings.Contains(errOut, "installed") {
		t.Errorf("the install did not run:\n%s", errOut)
	}
	if !strings.Contains(errOut, "keep sessions running after you log out") || internalWords.MatchString(errOut) {
		t.Errorf("the helper consent did not explain its changes in plain English:\n%s", errOut)
	}
}

// The agents that host add installs go in the prompter's log as they are
// installed: a cancel at the second install lists the first, and the end
// of the context counts as a cancel.
func TestHostAddCancelAfterAnAgentInstall(t *testing.T) {
	_, tr := withFake(t, nil)
	tr.SetAgents([]api.AgentCheck{{Kind: "claude", Title: "Claude Code", Support: "first-class"}, {Kind: "codex", Title: "Codex", Support: "first-class"}})
	var installed []agents.Agent
	defer func() { hostAddGuide = func(*guide) {} }()
	hostAddGuide = func(g *guide) {
		g.session = func(_ client.Host, a onboard.Action) (string, bool, error) {
			if len(installed) == 1 {
				// The second install: the request meets the end of the context.
				return "", false, context.Canceled
			}
			installed = append(installed, a.Agent)
			return "ss", true, nil
		}
		g.checks = func(client.Host) ([]api.AgentCheck, error) {
			var checks []api.AgentCheck
			for _, a := range installed {
				checks = append(checks, readyCheck(a.Kind, a.Title, api.LoginYes))
			}
			return checks, nil
		}
	}
	box := client.Host{Name: "box", Machine: &client.Machine{Provider: "fake", ID: "fake-box"}}
	var out bytes.Buffer
	_, err := said(t, func() error { return addHost(context.Background(), testPrompts("1 2\n", &out), box, false) })
	if len(installed) != 1 {
		t.Fatalf("installed %v\n%s", installed, out.String())
	}
	want := "Cancelled. Done before that: saved the host box, and installed " + installed[0].Title + " on box."
	if !isCancel(err) || err.Error() != want {
		t.Errorf("a cancel at the second install: %v, want %q\n%s", err, want, out.String())
	}
}

// setup with input off: a true message, and a Try: line from the command
// line with the flags that answer.
func TestSetupNoInputCommand(t *testing.T) {
	newTestHome(t)
	got, code := lineErr(t, "setup", "--no-input")
	if code != 4 || !strings.Contains(got, "Error: setup needs --host or --create, with --yes, to answer its questions, and input is off.") ||
		!strings.Contains(got, "Try: "+brand.Name+" setup --no-input --host <host> --tier <tier> --yes") {
		t.Errorf("setup --no-input: exit %d\n%s", code, got)
	}
	got, code = lineErr(t, "setup", "--host", "box", "--no-input")
	if code != 4 || !strings.Contains(got, "Try: "+brand.Name+" setup --host box --no-input --tier <tier> --yes") {
		t.Errorf("setup --host box --no-input: exit %d\n%s", code, got)
	}
}

// A Try: line puts the flag that answers before "--", never after it, where
// it would go to the session's command.
func TestTryLinesPutFlagsBeforeDash(t *testing.T) {
	_, tr := fakeBox(t, nil)
	q, _ := json.Marshal(api.FolderQuestion{Folder: "~/not-there"})
	tr.Fail("new", &proto.Error{Code: api.CodeNoFolder, Message: "the folder ~/not-there does not exist on this host", Hint: "Pass --mkdir.", Data: q})
	got, code := lineErr(t, "new", "box", "--json", "--", "htop", "-d", "5")
	if code != 2 || !strings.Contains(got, "Try: "+brand.Name+" new box --json --detach -- htop -d 5\n") {
		t.Errorf("new --json -- htop: exit %d\n%s", code, got)
	}
	got, code = lineErr(t, "new", "box", "--dir", "~/not-there", "--no-input", "--detach", "--wall", "none", "--downgrade", "--", "sleep", "60")
	if code != 4 || !strings.Contains(got, "Try: "+brand.Name+" new box --dir '~/not-there' --no-input --detach --wall none --downgrade --mkdir -- sleep 60\n") {
		t.Errorf("new with a missing folder and a command: exit %d\n%s", code, got)
	}
	// A "--" that is a flag's value is not the end of the flags.
	got, _ = lineErr(t, "new", "box", "--name", "--", "--json")
	if !strings.Contains(got, "Try: "+brand.Name+" new box --name -- --json --detach\n") {
		t.Errorf("new --name -- --json:\n%s", got)
	}
}
