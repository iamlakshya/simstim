package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/client/clienttest"
	"github.com/iamlakshya/simstim/internal/onboard"
	"github.com/iamlakshya/simstim/internal/proto"
)

// fakeBox saves the host box, a fake machine whose bridge tr serves.
func fakeBox(t *testing.T, sessions []client.Session) (client.Host, *clienttest.Transport) {
	t.Helper()
	_, tr := withFake(t, sessions)
	if err := saveHost(client.Host{Name: "box", Machine: &client.Machine{Provider: "fake", ID: "fake-box"}}); err != nil {
		t.Fatal(err)
	}
	h, err := client.FindHost("box")
	if err != nil {
		t.Fatal(err)
	}
	return h, tr
}

// withLoginAttach makes the attach of a login run fn, for the rest of the
// test.
func withLoginAttach(t *testing.T, fn func(id, name string) (bool, error)) {
	t.Helper()
	old := loginAttach
	loginAttach = func(_ context.Context, _ client.Host, id, name string) (bool, error) { return fn(id, name) }
	t.Cleanup(func() { loginAttach = old })
}

// A login session: its name, and when it started.
func loginRow(id, state string, at time.Time) client.Session {
	return client.Session{ID: id, Name: "login-claude", State: state, Alive: state != api.StateExited, CreatedAt: at}
}

// claudeCheck is the host's check of Claude Code: ready, with login.
func claudeCheck(login, how string) api.AgentCheck {
	c := readyCheck("claude", "Claude Code", login)
	c.LoginHow = how
	return c
}

// starts counts the sessions that the fake bridge was asked to start.
func starts(tr *clienttest.Transport) int {
	n := 0
	for _, op := range tr.Ops() {
		if op == "new" {
			n++
		}
	}
	return n
}

// The agent must be installed, and found by a login shell, before its
// login starts (cli-spec 12.3, rule 3). The fake counts the requests that
// start a session: there is none.
func TestAgentsLoginNeedsTheAgent(t *testing.T) {
	_, tr := fakeBox(t, nil)
	got, code := lineErr(t, "agents", "login", "claude", "box")
	if code != 1 || !strings.Contains(got, "Error: Claude Code is not installed on box.") || !strings.Contains(got, "Try: "+brand.Name+" agents install claude box\n") {
		t.Errorf("not installed: exit %d\n%s", code, got)
	}
	for _, bin := range []string{"/home/dev/.local/bin/claude", "/home/dev/o'connor/.local/bin/claude", "/home/dev/$(touch RAN)/claude", "/home/dev/`touch RAN`/claude"} {
		tr.SetAgents([]api.AgentCheck{{Kind: "claude", Title: "Claude Code", Installed: true, Path: bin}})
		got, code = lineErr(t, "agents", "login", "claude")
		if code != 1 || !strings.Contains(got, "at "+bin+", but a login shell does not find it") || !strings.Contains(got, "Try: "+onboard.PathHint(filepath.Dir(bin))+"\n") {
			t.Errorf("not on PATH (%s): exit %d\n%s", bin, code, got)
		}
	}
	ops := tr.Ops()
	if n := starts(tr); n != 0 || !slices.Contains(ops, "agents.check") {
		t.Errorf("the bridge got %v: %d starts, want none after the check", ops, n)
	}
}

// A login that runs is found again: the newer of two, and never one that
// ended (cli-spec 12.3, rule 4).
func TestRunningLogin(t *testing.T) {
	t0 := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	list := []client.Session{
		loginRow("new2", api.StateExited, t0.Add(3*time.Hour)),
		loginRow("err2", api.StateErrored, t0.Add(4*time.Hour)),
		loginRow("lost2", api.StateLost, t0.Add(5*time.Hour)),
		loginRow("old2", api.StateIdle, t0),
		{ID: "other", Name: "login-codex", State: api.StateWorking, CreatedAt: t0.Add(6 * time.Hour)},
		loginRow("mid2", api.StateWorking, t0.Add(time.Hour)),
	}
	if s, ok := runningLogin(list, "login-claude"); !ok || s.ID != "mid2" {
		t.Errorf("found %+v, %v; want mid2", s, ok)
	}
	if _, ok := runningLogin(list[:3], "login-claude"); ok {
		t.Error("an ended login was found")
	}
	// When an ended login-claude stays on the host, agents login starts a new
	// one, and names it by its ID.
	_, tr := fakeBox(t, list[:1])
	tr.SetAgents([]api.AgentCheck{claudeCheck(api.LoginNo, "")})
	out, errOut, code := mainRun(t, "agents", "login", "claude", "--host", "box")
	if code != 0 || out != "" || !strings.Contains(errOut, "Started box/fake2. Attach to finish the login: "+brand.Name+" attach --id -- box/fake2\n") {
		t.Errorf("after an ended login: exit %d, stdout %q, stderr:\n%s", code, out, errOut)
	}
}

// Two logins that start one after the other, each before the other's
// session is in a list, as two clients at the same moment: each names its
// own session by its ID, never by the name that both share.
func TestTwoLoginsNameTheirOwnSession(t *testing.T) {
	_, tr := fakeBox(t, nil) // its list never shows the sessions it starts
	tr.SetAgents([]api.AgentCheck{claudeCheck(api.LoginNo, "")})
	var said []string
	for range 2 {
		out, errOut, code := mainRun(t, "agents", "login", "claude", "box")
		if code != 0 || out != "" {
			t.Fatalf("agents login: exit %d\nstdout %q\nstderr %s", code, out, errOut)
		}
		said = append(said, errOut)
	}
	for i, want := range []string{"fake2", "fake3"} {
		line := "Started box/" + want + ". Attach to finish the login: " + brand.Name + " attach --id -- box/" + want + "\n"
		if said[i] != line {
			t.Errorf("login %d said %q, want %q", i+1, said[i], line)
		}
	}
}

// What a host sends prints cleaned: the agent's title is the agent
// table's, its path and reason lose their control characters, and a
// session ID that is not one that a host makes is refused before it is
// printed or put in a command. stderr holds no ESC.
func TestLoginPrintsHostTextClean(t *testing.T) {
	claude, _ := agents.Find("claude")
	hostile := api.AgentCheck{Kind: "claude", Title: "Claude\x1b[2J Code", Installed: true, OnPath: true,
		Path: "/p\x1b[31m/claude\x1b]52;c;aGk=\x07", Login: api.LoginNo, LoginHow: "says\x1b]0;x\x07 not\u202e"}
	osc := "x\x1b]52;c;aGk=\x07"
	t0 := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)

	h, tr := fakeBox(t, []client.Session{loginRow(osc, api.StateIdle, t0)})
	tr.SetAgents([]api.AgentCheck{hostile})
	errOut, err := said(t, func() error { return logIn(context.Background(), h, claude, false) })
	if err != nil || strings.Contains(errOut, "\x1b") || !strings.Contains(errOut, "box/fake2") {
		t.Errorf("a running login with a hostile ID was not ignored safely: %v\n%q", err, errOut)
	}
	var b strings.Builder
	printError(&b, checkSessionID("box", osc), nil, false, false)
	if strings.Contains(b.String(), "\x1b") || !strings.Contains(b.String(), "box sent a session ID that is not valid") {
		t.Errorf("the error prints %q", b.String())
	}

	h, tr = fakeBox(t, nil)
	tr.SetAgents([]api.AgentCheck{hostile})
	tr.NewID = osc
	if errOut, err = said(t, func() error { return logIn(context.Background(), h, claude, false) }); err == nil || strings.Contains(errOut, "\x1b") {
		t.Errorf("a new login with a hostile ID: %v\n%q", err, errOut)
	}

	h, tr = fakeBox(t, nil)
	tr.SetAgents([]api.AgentCheck{hostile})
	withLoginAttach(t, func(string, string) (bool, error) { return true, nil })
	errOut, err = said(t, func() error { return logIn(context.Background(), h, claude, true) })
	if err != nil || strings.ContainsAny(errOut, "\x1b\x07\u202e") || !strings.Contains(errOut, "! Claude Code      /p[31m/claude]52;c;aGk=, not logged in (says]0;x not)") {
		t.Errorf("the line after a login: %v\n%q", err, errOut)
	}

	// The lines of agents install and new that hold the host's text.
	_, tr = fakeBox(t, nil)
	tr.SetAgents([]api.AgentCheck{hostile})
	_, errOut, _ = mainRun(t, "agents", "install", "claude", "box")
	if strings.Contains(errOut, "\x1b") || !strings.Contains(errOut, "is already installed on box, at /p[31m/claude]52;c;aGk=.") {
		t.Errorf("agents install of an installed agent: %q", errOut)
	}
	tr.NewID = osc
	if _, errOut, code := mainRun(t, "agents", "install", "claude", "box", "--reinstall"); code != 1 || strings.Contains(errOut, "\x1b") || strings.Contains(errOut, "Started") {
		t.Errorf("agents install with a hostile ID: exit %d\n%q", code, errOut)
	}
	if _, errOut, code := mainRun(t, "new", "box", "--detach", "--wall", "none", "--downgrade"); code != 1 || strings.Contains(errOut, "\x1b") || strings.Contains(errOut, "Started") {
		t.Errorf("new with a hostile ID: exit %d\n%q", code, errOut)
	}
	if !validSessionID("hqtiovfg") || !validSessionID("fake2") || validSessionID("fake-1_A") || validSessionID("") || validSessionID("a/b") || validSessionID(strings.Repeat("a", 33)) {
		t.Error("validSessionID")
	}
}

// After a detach, the client asks whether the login still runs: a login
// that ended while the client checked gets the agent's line with its next
// step. A check that fails after a detach still ends with 0, since the
// attach ended with a detach (cli-spec 5.1).
func TestAfterADetach(t *testing.T) {
	claude, _ := agents.Find("claude")
	h, tr := fakeBox(t, nil)
	tr.SetAgents([]api.AgentCheck{claudeCheck(api.LoginNo, "`claude auth status` says not")})
	withLoginAttach(t, func(id, _ string) (bool, error) {
		tr.SetSessions([]client.Session{{ID: id, Name: "login-claude", State: api.StateExited}})
		return false, nil
	})
	errOut, err := said(t, func() error { return logIn(context.Background(), h, claude, true) })
	if err != nil || strings.Contains(errOut, "keeps running") || !strings.Contains(errOut, "Try: "+brand.Name+" agents login claude box\n") {
		t.Errorf("a login that ended after the detach: %v\n%s", err, errOut)
	}

	h, tr = fakeBox(t, nil)
	tr.SetAgents([]api.AgentCheck{claudeCheck(api.LoginNo, "")})
	withLoginAttach(t, func(string, string) (bool, error) {
		tr.Fail("agents.check", &proto.Error{Code: "busy", Message: "the host is busy"})
		return false, nil
	})
	errOut, err = said(t, func() error { return logIn(context.Background(), h, claude, true) })
	if err != nil || !strings.Contains(errOut, "The check of the login failed: the host is busy.\n") ||
		!strings.HasSuffix(errOut, "The login keeps running. Attach again: "+brand.Name+" attach --id -- box/fake2\n") {
		t.Errorf("a check that failed after the detach: %v\n%s", err, errOut)
	}
	// After the end of the login, a failed check is the command's error.
	tr.Fail("agents.check", nil)
	withLoginAttach(t, func(string, string) (bool, error) {
		tr.Fail("agents.check", &proto.Error{Code: "busy", Message: "the host is busy"})
		return true, nil
	})
	if errOut, err = said(t, func() error { return logIn(context.Background(), h, claude, true) }); err == nil || strings.Contains(errOut, "keeps running") {
		t.Errorf("a check that failed after the end: %v\n%s", err, errOut)
	}
}

// The text before the attach names the detach key of config.toml.
func TestLoginNamesTheDetachKey(t *testing.T) {
	claude, _ := agents.Find("claude")
	h, tr := fakeBox(t, nil)
	tr.SetAgents([]api.AgentCheck{claudeCheck(api.LoginNo, "")})
	if err := os.MkdirAll(client.ConfigDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(client.ConfigDir(), "config.toml"), []byte("[attach]\ndetach_key = \"ctrl+]\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	withLoginAttach(t, func(string, string) (bool, error) { return true, nil })
	errOut, err := said(t, func() error { return logIn(context.Background(), h, claude, true) })
	if err != nil || !strings.Contains(errOut, "Answer it there, or press ctrl+] to leave it running.") || strings.Contains(errOut, `ctrl+\`) {
		t.Errorf("a new login: %v\n%s", err, errOut)
	}
	// An attach to a login that runs names the key too.
	tr.SetSessions([]client.Session{loginRow("s2", api.StateIdle, time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC))})
	errOut, err = said(t, func() error { return logIn(context.Background(), h, claude, true) })
	if err != nil || !strings.Contains(errOut, "already runs in box/s2. Attaching to it. Answer it there, or press ctrl+] to leave it running.\n") || strings.Contains(errOut, `ctrl+\`) {
		t.Errorf("a login that runs: %v\n%s", err, errOut)
	}
}

// shell needs no login. Its Try: line names the host that the user typed,
// and a placeholder only when the user named none (cli-spec 4.2).
func TestShellLoginNamesTheHost(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for args, want := range map[string]string{
		"agents login shell":               brand.Name + " new <host> --agent shell\n",
		"agents login shell box":           brand.Name + " new box --agent shell\n",
		"agents login shell --host my-vps": brand.Name + " new my-vps --agent shell\n",
		// A name that no host could have is never repeated (cli-spec 4.2
		// rule 6): the line names the command that lists the hosts.
		"agents login shell --host=a;b":    brand.Name + " host ls\n",
		"agents login shell --host=--help": brand.Name + " host ls\n",
		"agents login shell --host=-x":     brand.Name + " host ls\n",
	} {
		got, code := lineErr(t, strings.Fields(args)...)
		if code != 2 || !strings.Contains(got, "Try: "+want) {
			t.Errorf("%s: exit %d\n%s", args, code, got)
		}
	}
}

// agents login --json starts the login without attaching, and prints its
// session object (cli-spec 7.1), with started. A login that runs is
// printed with started false, and no second session starts.
func TestAgentsLoginJSON(t *testing.T) {
	t0 := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	_, tr := fakeBox(t, nil)
	tr.SetAgents([]api.AgentCheck{claudeCheck(api.LoginNo, "")})
	out, errOut, code := mainRun(t, "agents", "login", "claude", "box", "--json")
	if code != 0 || errOut != "" {
		t.Fatalf("exit %d\nstdout %s\nstderr %q", code, out, errOut)
	}
	checkJSON(t, "agents login --json", out, false)
	var s map[string]any
	_ = json.Unmarshal([]byte(out), &s)
	if s["v"] != float64(1) || s["host"] != "box" || s["id"] != "fake2" || s["name"] != "login-claude" || s["address"] != "box/fake2" || s["started"] != true {
		t.Errorf("a new login: %s", out)
	}
	// A second login in a row, which the fake's list does not show the
	// first: each address names its own session, by its ID.
	out, _, code = mainRun(t, "agents", "login", "claude", "box", "--json")
	s = nil
	_ = json.Unmarshal([]byte(out), &s)
	if code != 0 || s["id"] != "fake3" || s["address"] != "box/fake3" || s["started"] != true {
		t.Errorf("a second login: exit %d\n%s", code, out)
	}

	_, tr = fakeBox(t, []client.Session{loginRow("s2", api.StateIdle, t0), loginRow("s3", api.StateExited, t0)})
	tr.SetAgents([]api.AgentCheck{claudeCheck(api.LoginNo, "")})
	out, _, code = mainRun(t, "agents", "login", "claude", "box", "--json")
	s = nil
	_ = json.Unmarshal([]byte(out), &s)
	if code != 0 || s["id"] != "s2" || s["address"] != "box/s2" || s["started"] != false || starts(tr) != 0 {
		t.Errorf("a login that runs: exit %d, %d starts\n%s", code, starts(tr), out)
	}

	// An error is one JSON line on stderr, and stdout stays empty.
	tr.SetAgents(nil)
	out, errOut, code = mainRun(t, "agents", "login", "claude", "box", "--json")
	if code != 1 || out != "" || strings.Count(errOut, "\n") != 1 || !json.Valid([]byte(errOut)) {
		t.Errorf("not installed: exit %d\nstdout %q\nstderr %q", code, out, errOut)
	}
}

// JSON escapes unsafe runes and preserves all decoded host fields.
func TestAgentsLoginJSONKeepsTheHostsText(t *testing.T) {
	row := loginRow("s2", api.StateIdle, time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC))
	row.Reason = "r\u009b2J\u202e"
	row.Cwd = "/home/dev/x\x1b]52;c;aGk=\x07"
	row.HostVersion = "1.0\u200b\u2066"
	row.Agent = "cus\U000E0041tom\u009d"
	row.Secrets = []string{"KEY\u061c"}
	_, tr := fakeBox(t, []client.Session{row})
	tr.SetAgents([]api.AgentCheck{claudeCheck(api.LoginNo, "")})
	out, errOut, code := mainRun(t, "agents", "login", "claude", "box", "--json")
	if code != 0 {
		t.Fatalf("exit %d\n%s%s", code, out, errOut)
	}
	assertSafeJSON(t, out)
	var s map[string]any
	if err := json.Unmarshal([]byte(out), &s); err != nil {
		t.Fatal(err)
	}
	if s["reason"] != row.Reason || s["cwd"] != row.Cwd || s["host_version"] != row.HostVersion || s["agent"] != row.Agent || fmt.Sprint(s["secrets"]) != fmt.Sprint(row.Secrets) {
		t.Errorf("JSON changed host fields: %s", out)
	}
}

// The output of agents login, from fixed checks, against a golden file
// (cli-spec 12.4).
func TestGoldenLogin(t *testing.T) {
	t0 := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	claude, _ := agents.Find("claude")
	var b strings.Builder
	show := func(name string, sessions []client.Session, attachOK bool, attach func(tr *clienttest.Transport, id string) bool) {
		h, tr := fakeBox(t, sessions)
		tr.SetAgents([]api.AgentCheck{claudeCheck(api.LoginNo, "`claude auth status` says not")})
		attached := ""
		withLoginAttach(t, func(id, _ string) (bool, error) {
			attached = id
			return attach(tr, id), nil
		})
		errOut, err := said(t, func() error { return logIn(context.Background(), h, claude, attachOK) })
		fmt.Fprintf(&b, "== %s\n%s", name, errOut)
		if attached != "" {
			fmt.Fprintf(&b, "-> attached to %s\n", attached)
		}
		if err != nil {
			fmt.Fprintf(&b, "-> %v\n", err)
		}
	}
	loggedIn := func(tr *clienttest.Transport, _ string) bool {
		tr.SetAgents([]api.AgentCheck{claudeCheck(api.LoginYes, "`claude auth status` says so")})
		return true
	}
	// A detach: the session still runs.
	detached := func(tr *clienttest.Transport, id string) bool {
		tr.SetSessions([]client.Session{loginRow(id, api.StateIdle, t0)})
		return false
	}
	show("a new login, logged in at its end", nil, true, loggedIn)
	show("a new login, left by a detach", nil, true, detached)
	show("a login that runs", []client.Session{loginRow("s2", api.StateIdle, t0)}, true, detached)
	show("input off", nil, false, nil)
	show("input off, a login that runs", []client.Session{loginRow("s2", api.StateIdle, t0)}, false, nil)
	show("input off, two logins that run", []client.Session{loginRow("s3", api.StateIdle, t0.Add(time.Minute)), loginRow("s2", api.StateIdle, t0)}, false, nil)
	got := strings.ReplaceAll(b.String(), brand.Name, "<name>")
	golden := filepath.Join("testdata", "login.golden")
	if *update {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("the output differs from %s (run go test ./internal/cli -run TestGoldenLogin -update, and read the diff):\n%s", golden, got)
	}
}

// loginStartedRE finds the session of a login that agents login started.
var loginStartedRE = regexp.MustCompile(`It runs in the session login-claude \(box/([A-Za-z0-9_-]+)\)\.`)
