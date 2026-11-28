package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	if err := saveHost(t.Context(), client.Host{Name: "box", Machine: &client.Machine{Provider: "fake", ID: "fake-box"}}); err != nil {
		t.Fatal(err)
	}
	h, err := client.FindHost("box")
	if err != nil {
		t.Fatal(err)
	}
	return h, tr
}

// withLoginAttach makes the attach of a login run fn, for the rest of the test.
func withLoginAttach(t *testing.T, fn func(id, name string) (bool, error)) {
	t.Helper()
	old := loginAttach
	loginAttach = func(_ context.Context, _ client.Host, id, name string) (bool, bool, error) {
		ended, err := fn(id, name)
		return ended, false, err
	}
	t.Cleanup(func() { loginAttach = old })
}

func loginRow(id, state string, at time.Time) client.Session {
	return client.Session{ID: id, Name: "login-claude", State: state, Alive: state != api.StateExited, CreatedAt: at}
}

func claudeCheck(login, how string) api.AgentCheck {
	c := readyCheck("claude", "Claude Code", login)
	c.LoginHow = how
	return c
}

func starts(tr *clienttest.Transport) int {
	n := 0
	for _, op := range tr.Requests() {
		if op == "new" {
			n++
		}
	}
	return n
}

// Unknown agents fail with exit 3 and suggestions; agents without Setup.Login
// fail with exit 2 and name LoginEnv (cli-spec 12.3 rules 1 and 2).
func TestAgentsLoginAgentErrors(t *testing.T) {
	fakeBox(t, nil)
	for _, c := range []struct {
		args []string
		code int
		has  []string
	}{
		{[]string{"agents", "login", "cluade", "box"}, 3, []string{`there is no agent called "cluade"`, `Did you mean "claude"?`, "Try: " + brand.Name + " agents login claude box\n"}},
		{[]string{"agents", "login", "aider", "box"}, 2, []string{"aider has no login of its own. It reads ANTHROPIC_API_KEY or OPENAI_API_KEY.", "Try: Add the key under [secrets] in config.toml, and pass it with --secret.\n"}},
		{[]string{"agents", "login", "shell", "box"}, 2, []string{"shell has no login of its own, and needs none.", "Try: " + brand.Name + " new box --agent shell\n"}},
		{[]string{"agents", "login", "shell"}, 2, []string{"shell has no login of its own, and needs none.", "Try: " + brand.Name + " new <host> --agent shell\n"}},
		{[]string{"agents", "login", "shell", "bad host"}, 2, []string{"shell has no login of its own, and needs none.", "Try: " + brand.Name + " host ls\n"}},
	} {
		got, code := lineErr(t, c.args...)
		if code != c.code {
			t.Errorf("%v: exit %d, want %d\n%s", c.args, code, c.code, got)
		}
		for _, want := range c.has {
			if !strings.Contains(got, want) {
				t.Errorf("%v: missing %q in\n%s", c.args, want, got)
			}
		}
	}
}

// The agent must be installed, and found by a login shell, before its login
// starts (cli-spec 12.3, rule 3).
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
	ops := tr.Requests()
	if n := starts(tr); n != 0 || !slices.Contains(ops, "agents.check") {
		t.Errorf("the bridge got %v: %d starts, want none after the check", ops, n)
	}
}

// A login that runs is found again: the newer of two, and never one that ended
// (cli-spec 12.3, rule 4).
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
	_, tr := fakeBox(t, list[:1])
	tr.SetAgents([]api.AgentCheck{claudeCheck(api.LoginNo, "")})
	out, errOut, code := mainRun(t, "agents", "login", "claude", "--host", "box")
	if code != 0 || out != "" || !strings.Contains(errOut, "Started box/fake2. Attach to finish the login: "+brand.Name+" attach box/fake2\n") {
		t.Errorf("after an ended login: exit %d, stdout %q, stderr:\n%s", code, out, errOut)
	}
}

// Two logins that start one after the other each name their own session ID.
func TestTwoLoginsNameTheirOwnSession(t *testing.T) {
	_, tr := fakeBox(t, nil)
	tr.NewIDs = []string{"fake2", "fake3"}
	tr.SetAgents([]api.AgentCheck{claudeCheck(api.LoginNo, "")})
	var saidLines []string
	for range 2 {
		out, errOut, code := mainRun(t, "agents", "login", "claude", "box")
		if code != 0 || out != "" {
			t.Fatalf("agents login: exit %d\nstdout %q\nstderr %s", code, out, errOut)
		}
		saidLines = append(saidLines, errOut)
	}
	for i, want := range []string{"fake2", "fake3"} {
		line := "Started box/" + want + ". Attach to finish the login: " + brand.Name + " attach box/" + want + "\n"
		if saidLines[i] != line {
			t.Errorf("login %d said %q, want %q", i+1, saidLines[i], line)
		}
	}
}

// What a host sends prints cleaned, and invalid session IDs are rejected before
// being printed or put in a command.
func TestLoginPrintsHostTextClean(t *testing.T) {
	claude, _ := agents.Find("claude")
	hostile := api.AgentCheck{
		Kind: "claude", Title: "Claude\x1b[2J Code", Installed: true, OnPath: true,
		Path: "/p\x1b[31m/claude\x1b]52;c;aGk=\x07", Login: api.LoginNo, LoginHow: "says\x1b]0;x\x07 not\u202e",
	}
	osc := "x\x1b]52;c;aGk=\x07"

	h, tr := fakeBox(t, nil)
	tr.SetAgents([]api.AgentCheck{hostile})
	tr.NewID = osc
	if errOut, err := said(t, func() error { return logIn(context.Background(), h, claude, false) }); err == nil || strings.Contains(errOut, "\x1b") {
		t.Errorf("a new login with a hostile ID: %v\n%q", err, errOut)
	}

	h, tr = fakeBox(t, nil)
	tr.SetAgents([]api.AgentCheck{hostile})
	withLoginAttach(t, func(string, string) (bool, error) { return true, nil })
	errOut, err := said(t, func() error { return logIn(context.Background(), h, claude, true) })
	if err != nil || strings.ContainsAny(errOut, "\x1b\x07\u202e") || !strings.Contains(errOut, "! Claude Code      /p[31m/claude]52;c;aGk=, not logged in (says]0;x not)") {
		t.Errorf("the line after a login: %v\n%q", err, errOut)
	}

	_, tr = fakeBox(t, nil)
	tr.SetAgents([]api.AgentCheck{hostile})
	_, errOut, _ = mainRun(t, "agents", "install", "claude", "box")
	if strings.Contains(errOut, "\x1b") || !strings.Contains(errOut, "is already installed on box, at /p[31m/claude]52;c;aGk=.") {
		t.Errorf("agents install of an installed agent: %q", errOut)
	}
}

// After a detach, the client checks whether the login still runs; a failed
// check after a detach still exits 0 (cli-spec 12.3 rule 7).
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
		t.Errorf("ended after detach: %v\n%s", err, errOut)
	}

	// Check fails after detach -> exits 0 with attach-again line.
	h, tr = fakeBox(t, nil)
	tr.SetAgents([]api.AgentCheck{claudeCheck(api.LoginNo, "")})
	withLoginAttach(t, func(_, _ string) (bool, error) {
		tr.Fail("agents.check", &proto.Error{Code: "bad_request", Message: "host dropped"})
		return false, nil
	})
	errOut, err = said(t, func() error { return logIn(context.Background(), h, claude, true) })
	if err != nil || !strings.Contains(errOut, "The check of the login failed: host dropped.") || !strings.Contains(errOut, "The login keeps running. Attach again: "+brand.Name+" attach box/fake2\n") {
		t.Errorf("failed check after detach: %v\n%s", err, errOut)
	}

	// Check fails after session ended -> returns the error.
	h, tr = fakeBox(t, nil)
	tr.SetAgents([]api.AgentCheck{claudeCheck(api.LoginNo, "")})
	withLoginAttach(t, func(_, _ string) (bool, error) {
		tr.Fail("agents.check", &proto.Error{Code: "bad_request", Message: "host dropped"})
		return true, nil
	})
	if _, err := said(t, func() error { return logIn(context.Background(), h, claude, true) }); err == nil {
		t.Error("failed check after ended session succeeded")
	}
}

// Hosts with a sandbox floor above none remind the user about [secrets] tokens
// such as CLAUDE_CODE_OAUTH_TOKEN from claude setup-token (cli-spec 12.3 rule 10).
func TestAgentsLoginWalledFloorNote(t *testing.T) {
	_, tr := withFake(t, nil)
	tr.SetAgents([]api.AgentCheck{claudeCheck(api.LoginNo, "")})
	if err := saveHost(t.Context(), client.Host{
		Name:      "walled",
		WallFloor: api.LevelProcess,
		Machine:   &client.Machine{Provider: "fake", ID: "fake-walled"},
	}); err != nil {
		t.Fatal(err)
	}
	out, errOut, code := mainRun(t, "agents", "login", "claude", "walled")
	if code != 0 || out != "" {
		t.Fatalf("walled login: exit %d, stdout %q, stderr:\n%s", code, out, errOut)
	}
	for _, want := range []string{"sandbox floor process", "CLAUDE_CODE_OAUTH_TOKEN", "claude setup-token", "--secret"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("walled login lacks %q in:\n%s", want, errOut)
		}
	}
	// With --json, stderr has no plain text note.
	out, errOut, code = mainRun(t, "agents", "login", "claude", "walled", "--json")
	if code != 0 || errOut != "" || !json.Valid([]byte(out)) {
		t.Errorf("walled login --json: exit %d, stdout %q, stderr %q", code, out, errOut)
	}
}

// agents login --json prints a session object with v=1, address=host/id and
// started=true|false, never attaches, and physically escapes unsafe runes
// while keeping decoded values exact (cli-spec 7.1, 7.4, 12.3 rule 12).
func TestAgentsLoginJSON(t *testing.T) {
	t0 := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	_, tr := fakeBox(t, nil)
	tr.NewIDs = []string{"fake2", "fake3"}
	tr.SetAgents([]api.AgentCheck{claudeCheck(api.LoginNo, "")})
	out, errOut, code := mainRun(t, "agents", "login", "claude", "box", "--json")
	if code != 0 || errOut != "" {
		t.Fatalf("agents login --json: exit %d\nstdout %s\nstderr %s", code, out, errOut)
	}
	checkJSON(t, "agents login --json", out, false)
	var s map[string]any
	_ = json.Unmarshal([]byte(out), &s)
	if s["v"] != float64(1) || s["host"] != "box" || s["id"] != "fake2" || s["name"] != "login-claude" || s["address"] != "box/fake2" || s["started"] != true {
		t.Errorf("a new login: %s", out)
	}

	// Second login in a row (fake list stays empty): each address names its own ID.
	out, _, code = mainRun(t, "agents", "login", "claude", "box", "--json")
	s = nil
	_ = json.Unmarshal([]byte(out), &s)
	if code != 0 || s["id"] != "fake3" || s["address"] != "box/fake3" || s["started"] != true {
		t.Errorf("a second login: exit %d\n%s", code, out)
	}

	// Running login: started=false, 0 starts.
	_, tr = fakeBox(t, []client.Session{loginRow("s2", api.StateIdle, t0), loginRow("s3", api.StateExited, t0)})
	tr.SetAgents([]api.AgentCheck{claudeCheck(api.LoginNo, "")})
	out, _, code = mainRun(t, "agents", "login", "claude", "box", "--json")
	s = nil
	_ = json.Unmarshal([]byte(out), &s)
	if code != 0 || s["id"] != "s2" || s["address"] != "box/s2" || s["started"] != false || starts(tr) != 0 {
		t.Errorf("a login that runs: exit %d, %d starts\n%s", code, starts(tr), out)
	}

	// Error is one JSON line on stderr, stdout stays empty.
	tr.SetAgents(nil)
	out, errOut, code = mainRun(t, "agents", "login", "claude", "box", "--json")
	if code != 1 || out != "" || strings.Count(errOut, "\n") != 1 || !json.Valid([]byte(errOut)) {
		t.Errorf("not installed: exit %d\nstdout %q\nstderr %q", code, out, errOut)
	}
}

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
	for _, r := range out {
		if r != '\n' && r != '\t' && api.Unclean(r) {
			t.Fatalf("JSON wrote raw unsafe rune %U:\n%s", r, out)
		}
	}
	var s map[string]any
	if err := json.Unmarshal([]byte(out), &s); err != nil {
		t.Fatal(err)
	}
	if s["reason"] != row.Reason || s["cwd"] != row.Cwd || s["host_version"] != row.HostVersion || s["agent"] != row.Agent || fmt.Sprint(s["secrets"]) != fmt.Sprint(row.Secrets) {
		t.Errorf("JSON changed host fields: %s", out)
	}
}

// Golden test of agents login output (cli-spec 12.4).
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
