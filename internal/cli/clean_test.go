package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/client/clienttest"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/provider"
)

// hostile is text that a host could send: an OSC 52 write to the clipboard,
// a bidirectional override, a zero-width space and a tag character.
const hostile = "\x1b]52;c;aGk=\x07Allow\u202e this\u200b?\U000e0041"

// noHostBits fails when out holds a control or a hidden character that a
// host sent.
func noHostBits(t *testing.T, what, out string) {
	t.Helper()
	for _, b := range []string{"\x1b", "\x07", "\u202e", "\u200b", "\U000e0041", "\u009b"} {
		if strings.Contains(out, b) {
			t.Errorf("%s holds %q from the host:\n%q", what, b, out)
		}
	}
}

// ls prints what a host sent as plain text, and keeps it for --json.
func TestLsCleansHostText(t *testing.T) {
	results := []hostSessions{
		{Host: "box", Sessions: []client.Session{
			{ID: "a1\x1b[2J", Name: "app\u202egnp.exe", Agent: "claude", Cwd: "/srv/app\x07", State: api.StateLost, Reason: hostile, CreatedAt: time.Now()},
		}},
		{Host: "far", Error: "cannot reach far: " + hostile},
	}
	out, _ := captureOutput(t, func() { printSessions(results) })
	noHostBits(t, "ls", out)
	for _, want := range []string{"a1[2J", "appgnp.exe", "/srv/app\n", "lost (]52;c;aGk=Allow this?)", "far  ◌ cannot reach far: ]52;c;aGk=Allow this?"} {
		if !strings.Contains(out, want) {
			t.Errorf("ls does not show %q:\n%s", want, out)
		}
	}
	if results[0].Sessions[0].Reason != hostile || results[1].Error == "" {
		t.Error("ls changed the host's own values, which --json prints")
	}
	err := endedError(client.Host{Name: "box"}, results[0].Sessions[0])
	noHostBits(t, "endedError", err.Error())
	var ue *client.UserError
	if errors.As(err, &ue) {
		noHostBits(t, "endedError's step", ue.Hint)
	}
}

// host doctor --fix prints a host's fixes as plain text, and applies them
// by the IDs that the host sent.
func TestOfferFixesCleansHostText(t *testing.T) {
	fixes := []api.Fix{{ID: "x\u200b", Problem: "linger is off" + hostile, Action: "turn on\x1b[2J linger", Command: "loginctl\u202e enable-linger me"}}
	var out bytes.Buffer
	var asked []string
	err := offerFixes(testPrompts("y\n", &out), qFix, fixes, false, func(ids []string) ([]api.Fix, error) {
		asked = ids
		res := append([]api.Fix(nil), fixes...)
		res[0].Error = "it failed: " + hostile
		return res, nil
	})
	if err == nil {
		t.Fatal("a failed fix did not fail")
	}
	noHostBits(t, "the fixes", out.String())
	if !strings.Contains(out.String(), "Runs: loginctl enable-linger me") {
		t.Errorf("the command is not shown as plain text:\n%s", out.String())
	}
	if len(asked) != 1 || asked[0] != "x\u200b" {
		t.Errorf("applied %q, want the host's own ID", asked)
	}
}

// A question about a path from the host shows it cleaned, or quoted when
// it holds hidden characters, so the user sees them before answering.
func TestQuestionsShowHostPaths(t *testing.T) {
	var out bytes.Buffer
	q := client.SetupQuestion{Repo: "/home/dev/src/app\x1b]52;c;aGk=\x07\u202e", Script: "/home/dev/src/app/.setup\u200b"}
	// Each wording of the setup question, and the note without a terminal.
	for _, past := range []client.SetupPast{client.SetupNotTrusted, client.SetupChanged, client.SetupUnchecked} {
		_, _ = askSetup(testPrompts("n\n", &out), "box", q, past)
		out.WriteString(skipNote("box", q, past) + "\n")
	}
	_, _ = askMakeFolder(testPrompts("n\n", &out), "box", client.FolderQuestion{Folder: "/home/dev/src/mi\u200cnd"})
	noHostBits(t, "the questions", out.String())
	for _, want := range []string{`"/home/dev/src/app\x1b]52;c;aGk=\a\u202e"`, `"/home/dev/src/app/.setup\u200b"`, `"/home/dev/src/mi\u200cnd"`} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the questions do not show %s:\n%s", want, out.String())
		}
	}
	out.Reset()
	_, _ = askMakeFolder(testPrompts("n\n", &out), "box", client.FolderQuestion{Folder: "/home/dev/src/new-app"})
	if !strings.Contains(out.String(), "The folder /home/dev/src/new-app does not exist") {
		t.Errorf("a clean folder is quoted:\n%s", out.String())
	}
}

// ports and host restore print a process's name and a session's text as
// plain text. A process names itself, so ESC c (a full reset) could be
// its name.
func TestPortsAndRestoreCleanHostText(t *testing.T) {
	var out bytes.Buffer
	printPorts(&out, []client.Port{{Port: 3000, Address: "127.0.0.1\u200b", PID: 7, Process: "\x1bcnode"}})
	printRestore(&out, []api.RestoreResult{{ID: "abc", Name: "app" + hostile, Action: "failed", Why: hostile, Hint: "do\x1bc " + hostile}})
	noHostBits(t, "ports and restore", out.String())
	if !strings.Contains(out.String(), "cnode (pid 7)") || !strings.Contains(out.String(), "app]52;c;aGk=Allow this?") ||
		!strings.Contains(out.String(), "    Try: doc ]52;c;aGk=Allow this?\n") {
		t.Errorf("ports and restore lost the host's text:\n%s", out.String())
	}
}

// host alerts prints a host's alerts file and its delivery log as plain
// text.
func TestAlertsCleanHostText(t *testing.T) {
	info := client.AlertsInfo{Path: "/home/dev/.config/alerts.toml\u202e", Exists: true, Error: hostile, Hint: hostile,
		Config: &client.AlertsConfig{Events: []string{"needs_input\x07"}, Delay: "10s\u200b", MinTurn: "60s",
			Destinations: []client.AlertDestination{{Type: "command", Run: "notify\x1b]52;c;aGk=\x07", Events: []string{hostile}},
				{Type: "ntfy", URL: "https://ntfy.sh/x\u202e", TokenFile: "~/t\u200b"}}}}
	out, _ := captureOutput(t, func() {
		printAlerts("box", info)
		printAlertLog([]client.AlertLogEntry{{Event: "needs_input", Session: "app" + hostile, Type: "command", Error: hostile}})
	})
	noHostBits(t, "host alerts", out)
	if !strings.Contains(out, "notify]52;c;aGk=") || !strings.Contains(out, "app]52;c;aGk=A") {
		t.Errorf("host alerts lost the host's text:\n%s", out)
	}
	if info.Config.Destinations[0].Run != "notify\x1b]52;c;aGk=\x07" {
		t.Error("printing changed the host's own values")
	}
}

// The end of an attach prints the session's name and the signal that the
// host sent as plain text.
func TestEndMessageCleansHostText(t *testing.T) {
	out := endMessage("app"+hostile, "box", "abc", &proto.Ended{ExitCode: 137, Signal: "killed" + hostile})
	noHostBits(t, "the end message", out)
	if !strings.Contains(out, "The session app]52;c;aGk=Allow this? on box ended (killed]52;c;aGk=Allow this?)") {
		t.Errorf("end message = %q", out)
	}
}

// The agent checks, as host add, the guided setup, agent install and host
// doctor print them, are plain text.
func TestAgentChecksCleanHostText(t *testing.T) {
	var out bytes.Buffer
	printAgentChecks(&out, "box", []api.AgentCheck{{Kind: "claude", Title: "Claude Code\x1b[2J", Support: "first-class",
		Installed: true, Path: "/usr/bin/claude\u202e", Login: api.LoginUnknown, LoginHow: hostile}})
	noHostBits(t, "the agent checks", out.String())
}

// printJSON keeps the host's values, and escapes each character that a
// terminal could act on or that hides text.
func TestPrintJSONEscapesHostText(t *testing.T) {
	v := map[string]string{"reason": "a\u009b2Jb\u202ec\u200bd\U000e0041e\x1b]52;c;x\x07f\x7fg\u00e9h\u2028"}
	var err error
	out, _ := captureOutput(t, func() { err = printJSON(v) })
	if err != nil {
		t.Fatal(err)
	}
	noHostBits(t, "printJSON", out)
	for _, r := range out {
		if r >= 0x7f && api.Unclean(r) {
			t.Errorf("printJSON wrote %U raw: %q", r, out)
		}
	}
	if !strings.Contains(out, `\u009b`) || !strings.Contains(out, `\udb40\udc41`) || !strings.Contains(out, "\u00e9") {
		t.Errorf("printJSON = %q", out)
	}
	var back struct {
		V      int    `json:"v"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(out), &back); err != nil || back.V != 1 || back.Reason != v["reason"] {
		t.Fatalf("printJSON decodes to %+v, %v, want v=1 and the host's own value", back, err)
	}
	clean := []byte("{\n  \"a\": \"caf\u00e9 \u2192 ok\"\n}\n")
	if got := jsonSafe(clean); &got[0] != &clean[0] {
		t.Error("jsonSafe copied JSON that needs no escape")
	}
}

// hostileBridge saves the host box, which the fake provider made, and whose
// bridge answers each request with hostile text of the host's own.
func hostileBridge(t *testing.T) *clienttest.Transport {
	t.Helper()
	_, tr := withFake(t, []client.Session{{ID: "abc", Name: "app" + hostile, Agent: "shell", State: api.StateWorking, Alive: true}})
	tr.Features = []string{"alerts", api.FeatureDebug, "agents.check"}
	tr.Uname = "Linux riscv\x1b]52;c;aGk=\x07\u202e"
	tr.Answers = map[string]any{
		"ports":            []api.Port{{Port: 3000, Address: "127.0.0.1\u200b", PID: 7, Process: "\x1bcnode"}},
		"restore":          []api.RestoreResult{{ID: "abc", Name: "app" + hostile, Action: "failed", Why: hostile, Hint: "do\x1bc " + hostile}},
		api.OpDebugSave:    api.DebugFile{Path: "/tmp/save" + hostile, Size: 10},
		api.OpDebugProfile: api.DebugFile{Path: "/tmp/prof" + hostile, Size: 10},
		"alerts.test":      []api.AlertResult{{Type: "command", Target: "notify" + hostile, Error: hostile}},
		"setup":            api.SetupResult{Steps: []api.Check{{Name: "linger" + hostile, Detail: hostile, Fix: hostile}}},
		"doctor":           api.HostInfo{Distro: "Debian" + hostile, CPUs: 2, MemTotal: 4 << 30, Agents: map[string]string{"shell": "/bin/sh"}},
		"agents.check": []api.AgentCheck{{Kind: "claude", Title: "Claude Code", Support: "first-class", Installed: true, OnPath: true,
			Path: "/usr/bin/claude" + hostile, Login: api.LoginYes}},
	}
	return tr
}

// The commands print what a host answers as plain text, through their own
// paths: ports, host restore, debug save and profile, host alerts test, and
// agent install of an agent that is there.
func TestCommandsCleanHostAnswers(t *testing.T) {
	hostileBridge(t)
	if err := client.SaveHostsContext(context.Background(), []client.Host{{Name: "box", Machine: &client.Machine{Provider: "fake", ID: "fake-box"}}}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"ports", "box/abc"}, "cnode (pid 7)"},
		{[]string{"host", "restore", "box"}, "    Try: doc ]52;c;aGk=Allow this?\n"},
		{[]string{"debug", "save", "box/abc"}, "/tmp/save]52;c;aGk=Allow this?"},
		{[]string{"debug", "profile", "box/abc", "heap"}, "/tmp/prof]52;c;aGk=Allow this?"},
		{[]string{"host", "alerts", "test", "box"}, "notify]52;c;aGk=Allow this?"},
		{[]string{"agents", "install", "claude", "--host", "box"}, "at /usr/bin/claude]52;c;aGk=Allow this?."},
	} {
		what := strings.Join(c.args, " ")
		out, errOut, err := runApart(t, c.args...)
		out += errOut // Every emitted host value is checked, on either stream.
		var ue *client.UserError
		if errors.As(err, &ue) {
			noHostBits(t, what+"'s error", ue.Msg+ue.Hint)
		}
		noHostBits(t, what, out)
		if !strings.Contains(out, c.want) {
			t.Errorf("%s does not show %q:\n%s (%v)", what, c.want, out, err)
		}
	}
}

// host create, host add and the guided setup connect through connectHost,
// which prints the host's platform, its setup steps and its system as
// plain text.
func TestConnectHostCleansHostText(t *testing.T) {
	hostileBridge(t)
	p, err := findProvider("fake")
	if err != nil {
		t.Fatal(err)
	}
	out, errOut := captureOutput(t, func() {
		err = createHost(context.Background(), p, provider.Request{Name: "box", Out: os.Stderr})
	})
	out += errOut
	if err != nil {
		t.Fatal(err)
	}
	noHostBits(t, "host create", out)
	for _, want := range []string{"linux/riscv]52;c;aGk=", "linger]52;c;aGk=Allow this?", "✓ Debian]52;c;aGk=Allow this?, 2 CPUs"} {
		if !strings.Contains(out, want) {
			t.Errorf("host create does not show %q:\n%s", want, out)
		}
	}
}

// host destroy refuses while a session works, and names it. Its name is
// the host's text: the refusal shows it cleaned.
func TestDestroyRefusalCleansHostText(t *testing.T) {
	m, _ := withFake(t, []client.Session{{ID: "abc", Name: "app\x1bc" + hostile, State: api.StateWorking, Alive: true}})
	if err := saveHostContext(context.Background(), client.Host{Name: "box", Machine: &client.Machine{Provider: "fake", ID: "fake-box"}}); err != nil {
		t.Fatal(err)
	}
	err := removeHost(context.Background(), testPrompts("", &bytes.Buffer{}), destroyOpts("box", false))
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Msg != "1 session on box is working: appc]52;c;aGk=Allow this?" {
		t.Fatalf("err = %v, want a refusal that names the working session", err)
	}
	noHostBits(t, "host destroy's refusal", ue.Msg)
	for _, action := range m.Calls() {
		if action == "destroy" {
			t.Fatal("the refused destroy changed the machine")
		}
	}
}

// printError prints every error cleaned, whatever built it: a user error
// made in a place that does no cleaning of its own, with a message of two
// lines, and a plain error.
func TestPrintErrorCleansEveryError(t *testing.T) {
	printed := func(err error) string {
		t.Helper()
		var b strings.Builder
		printError(&b, err, nil, false, false)
		return b.String()
	}
	out := printed(&client.UserError{Msg: "made here: \x1bc" + hostile + "\nline two" + hostile, Hint: "do\x1b]52;c;aGk=\x07 this"})
	noHostBits(t, "a user error", out)
	if !strings.Contains(out, "made here: c]52;c;aGk=Allow this?\nline two]52;c;aGk=Allow this?\n") || !strings.Contains(out, "do]52;c;aGk= this") {
		t.Errorf("printError = %q", out)
	}
	out = printed(errors.New("plain " + hostile))
	noHostBits(t, "a plain error", out)
	if !strings.Contains(out, "plain ]52;c;aGk=Allow this?") {
		t.Errorf("printError = %q", out)
	}
}
