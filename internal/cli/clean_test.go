package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/client/clienttest"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
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
	out, _ := stdout(t, func() error { printSessions(results); return nil })
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
	err := offerFixes(strings.NewReader("y\n"), &out, fixes, false, func(ids []string) ([]api.Fix, error) {
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

// The session commands print a session's name, which the host keeps as it
// was given, as plain text: new --detach, ports, send, debug, rename, kill,
// resume, and attach to an agent that has ended.
func TestSessionCommandsCleanHostText(t *testing.T) {
	newTestHost(t)
	const name = "app\x1b]52;c;aGk=\x07\u202egnp"
	const shown = "app]52;c;aGk=gnp"
	check := func(what, out string) {
		t.Helper()
		noHostBits(t, what, out)
		if !strings.Contains(out, shown) {
			t.Errorf("%s does not show the name %q:\n%s", what, shown, out)
		}
	}
	out := ok(t, "new", "box", "--detach", "--name", name, "--", "/bin/sh", "-c", "exec sleep 60")
	check("new --detach", out)
	id := startedRE.FindStringSubmatch(out)[1]
	check("ports", ok(t, "ports", "box/"+id))
	check("send", ok(t, "send", "box/"+id, "true", "--no-enter"))
	out = ok(t, "debug", "start", "box/"+id, "--for", "10m")
	check("debug start", out)
	if !strings.Contains(out, "debug stop box/"+id) {
		t.Errorf("debug start names the session by its name, which is not the one shown:\n%s", out)
	}
	check("debug stop", ok(t, "debug", "stop", "box/"+id))
	check("kill", ok(t, "kill", "box/"+id))

	id = newSession(t, name, "/bin/sh", "-c", "exec sleep 60")
	check("rename", ok(t, "rename", "box/"+id, "plain"))

	id = newSession(t, name, "/bin/sh", "-c", "sleep 1; exit 2")
	agentEnded(t, id)
	_, err := run(t, "attach", "box/"+id)
	var ue *client.UserError
	if !errors.As(err, &ue) {
		t.Fatalf("attach to an ended agent = %v", err)
	}
	check("attach to an ended agent", ue.Msg)
	check("resume", ok(t, "resume", "box/"+id, "--detach"))
}

// A question about a path from the host shows it cleaned, or quoted when
// it holds hidden characters, so the user sees them before answering.
func TestQuestionsShowHostPaths(t *testing.T) {
	var out bytes.Buffer
	q := client.SetupQuestion{Repo: "/home/dev/src/app\x1b]52;c;aGk=\x07\u202e", Script: "/home/dev/src/app/.setup\u200b"}
	// Each wording of the setup question, and the note without a terminal.
	for _, past := range []client.SetupPast{client.SetupNotTrusted, client.SetupChanged, client.SetupUnchecked} {
		askSetup(strings.NewReader("n\n"), &out, "box", q, past)
		out.WriteString(skipNote("box", q, past) + "\n")
	}
	askMakeFolder(strings.NewReader("n\n"), &out, "box", client.FolderQuestion{Folder: "/home/dev/src/mi\u200cnd"})
	noHostBits(t, "the questions", out.String())
	for _, want := range []string{`"/home/dev/src/app\x1b]52;c;aGk=\a\u202e"`, `"/home/dev/src/app/.setup\u200b"`, `"/home/dev/src/mi\u200cnd"`} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the questions do not show %s:\n%s", want, out.String())
		}
	}
	out.Reset()
	askMakeFolder(strings.NewReader("n\n"), &out, "box", client.FolderQuestion{Folder: "/home/dev/src/new-app"})
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
	out, _ := stdout(t, func() error {
		printAlerts("box", info)
		printAlertLog([]client.AlertLogEntry{{Event: "needs_input", Session: "app" + hostile, Type: "command", Error: hostile}})
		return nil
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
	out, err := stdout(t, func() error { return printJSON(v) })
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
	var back map[string]string
	if err := json.Unmarshal([]byte(out), &back); err != nil || back["reason"] != v["reason"] {
		t.Fatalf("printJSON decodes to %q, %v, want the host's own value", back["reason"], err)
	}
	clean := []byte("{\n  \"a\": \"caf\u00e9 \u2192 ok\"\n}\n")
	if got := jsonSafe(clean); &got[0] != &clean[0] {
		t.Error("jsonSafe copied JSON that needs no escape")
	}
}

// The commands that a command prints for the user to run next name the
// session by its ID. A name is the host's text, and could be shell syntax
// that would run on the laptop when the command is pasted.
func TestPrintedCommandsNameTheID(t *testing.T) {
	newTestHost(t)
	const name = "$(touch${IFS}owned)`id`;a && b | c\nd"
	inert := func(what, cmd string) {
		t.Helper()
		if strings.ContainsAny(cmd, "$`;&|\n") || strings.Contains(cmd, "touch") {
			t.Errorf("%s holds the session's name: %q", what, cmd)
		}
	}
	id := newSession(t, name, "/bin/sh", "-c", "exec sleep 60")
	out := ok(t, "debug", "start", "box/"+id, "--for", "10m")
	stop := out[strings.LastIndex(out, "To stop: "):]
	if stop != "To stop: "+brand.Name+" debug stop box/"+id+"\n" {
		t.Errorf("debug start = %q", stop)
	}
	_, err := run(t, "resume", "box/"+id, "--detach")
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Hint != brand.Name+" attach box/"+id {
		t.Fatalf("resume of a running agent = %v", err)
	}
	inert("resume's step", ue.Hint)

	ended := newSession(t, name, "/bin/sh", "-c", "sleep 1; exit 2")
	agentEnded(t, ended)
	_, err = run(t, "attach", "box/"+ended)
	if !errors.As(err, &ue) || !strings.HasPrefix(ue.Hint, brand.Name+" resume box/"+ended) {
		t.Fatalf("attach to an ended agent = %v", err)
	}
	inert("attach's step", ue.Hint)
	if got := endMessage(name, "box", ended, &proto.Ended{ExitCode: 137, OOM: true}); !strings.Contains(got, "Try: "+brand.Name+" resume box/"+ended+" ") {
		t.Errorf("the end message's step = %q", got)
	}
}

// A clean filter in the session's repository writes the OSC 52 clipboard
// sequence and more to stderr, and fails. git's error, which pull prints,
// passes the cleaner: the text arrives, without its control bytes.
func TestPullCleansAFiltersError(t *testing.T) {
	hosttest.SkipOnGit254(t)
	th := newTestHost(t)
	th.h.Install(client.HelperVersion(), "")
	remote := filepath.Join(th.h.Home, "src", "app")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	git := func(dir string, args ...string) {
		t.Helper()
		if out, err := runGit(dir, args...); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	for name, body := range map[string]string{"README": "app\n", ".gitattributes": "README filter=evil\n"} {
		if err := os.WriteFile(filepath.Join(remote, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(remote, "init", "-q", "-b", "main")
	git(remote, "add", "README", ".gitattributes")
	git(remote, "commit", "-q", "-m", "first")
	said := filepath.Join(th.h.Root, "said")
	filter := filepath.Join(th.h.Root, "evil.sh")
	if err := os.WriteFile(said, []byte("fatal: "+hostile), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filter, []byte("#!/bin/sh\ncat "+said+" >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	git(remote, "config", "filter.evil.clean", filter)
	git(remote, "config", "filter.evil.required", "true")
	// A new time, so that git status reads the file through the filter.
	later := time.Now().Add(10 * time.Second)
	if err := os.Chtimes(filepath.Join(remote, "README"), later, later); err != nil {
		t.Fatal(err)
	}
	m := startedRE.FindStringSubmatch(ok(t, "new", "box", "--detach", "--name", "evil", "--dir", remote, "--", "sleep", "60"))
	if m == nil {
		t.Fatal("new did not start the session")
	}
	local := t.TempDir()
	git(local, "init", "-q", "-b", "main")
	t.Chdir(local)
	_, err := run(t, "pull", "box/"+m[1])
	var ue *client.UserError
	if !errors.As(err, &ue) || !strings.Contains(ue.Msg, "fatal: ]52;c;aGk=Allow this?") || ue.Hint == "" {
		t.Fatalf("pull = %v, want git's error with the filter's text, cleaned", err)
	}
	noHostBits(t, "pull's error", ue.Msg+"\n"+ue.Hint)
}

// pull's two early refusals print the session's name and its branch, both
// the host's text, as plain text: a session with no branch, and a branch
// that is checked out here.
func TestPullRefusalsCleanHostText(t *testing.T) {
	// pull asks the helper about the repository (git-op repo.info), which
	// git 2.54 may not count the changes of.
	hosttest.SkipOnGit254(t)
	th := newTestHost(t)
	th.h.Install(client.HelperVersion(), "")
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.CommandContext(context.Background(), "git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	const branch = "feat\u202eppa\u200b"
	remote, local := t.TempDir(), t.TempDir()
	for _, dir := range []string{remote, local} {
		git(dir, "init", "-q", "-b", branch)
		git(dir, "commit", "-q", "--allow-empty", "-m", "one")
	}
	t.Chdir(local)
	out := ok(t, "new", "box", "--detach", "--name", "app"+hostile, "--dir", remote, "--", "/bin/sh", "-c", "exec sleep 60")
	id := startedRE.FindStringSubmatch(out)[1]
	refusal := func(what, want string) {
		t.Helper()
		_, err := run(t, "pull", "box/"+id)
		var ue *client.UserError
		if !errors.As(err, &ue) || !strings.Contains(ue.Msg, want) {
			t.Fatalf("pull (%s) = %v, want %q", what, err, want)
		}
		noHostBits(t, what, ue.Msg)
	}
	refusal("a branch checked out here", "featppa is the branch you have checked out here")
	git(remote, "checkout", "-q", "--detach")
	refusal("a detached HEAD", "app]52;c;aGk=Allow this? has no branch checked out")
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
	if err := client.SaveHosts([]client.Host{{Name: "box", Machine: &client.Machine{Provider: "fake", ID: "fake-box"}}}); err != nil {
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
		out, err := run(t, c.args...)
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
	out, err := stdout(t, func() error {
		return createHost(context.Background(), p, provider.Request{Name: "box", Out: os.Stdout})
	})
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
	withFake(t, []client.Session{{ID: "abc", Name: "app\x1bc" + hostile, State: api.StateWorking, Alive: true}})
	if err := saveHost(client.Host{Name: "box", Machine: &client.Machine{Provider: "fake", ID: "fake-box"}}); err != nil {
		t.Fatal(err)
	}
	err := destroyHost(context.Background(), "box", false, true)
	var ue *client.UserError
	if !errors.As(err, &ue) || !strings.Contains(ue.Msg, "are working: appc]52;c;aGk=Allow this?") {
		t.Fatalf("err = %v, want a refusal that names the working session", err)
	}
	noHostBits(t, "host destroy's refusal", ue.Msg)
}

// printError prints every error cleaned, whatever built it: a user error
// made in a place that does no cleaning of its own, with a message of two
// lines, and a plain error.
func TestPrintErrorCleansEveryError(t *testing.T) {
	printed := func(err error) string {
		t.Helper()
		f, ferr := os.CreateTemp(t.TempDir(), "stderr")
		if ferr != nil {
			t.Fatal(ferr)
		}
		old := os.Stderr
		os.Stderr = f
		printError(err)
		os.Stderr = old
		_ = f.Close()
		b, _ := os.ReadFile(f.Name())
		return string(b)
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
