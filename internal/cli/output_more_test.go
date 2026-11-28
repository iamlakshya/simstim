package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/diag"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
)

// A word of the form of an ID, with a digit, finds a session by its ID
// alone, never by a name (cli-spec 9.2), on a host's own list and across
// the hosts' lists. A word of another form, such as a name of 8 letters,
// finds an ID first, then a name. With no session of that ID, the word is
// not found, with the sessions that it may mean by host/ID. The JSON
// address is always host/ID (cli-spec 7.1).
func TestAWordOfAnIDsFormIsAnID(t *testing.T) {
	list := []client.Session{
		{ID: "abcd2345", Name: "web"},
		{ID: "zzzz2345", Name: "abcd2345"}, // an older name: the other's ID
		{ID: "c3c3c3c3", Name: "qqqq2345"}, // an older name of an ID's form
		{ID: "d4d4d4d4", Name: "dup"},
		{ID: "e5e5e5e5", Name: "f6"},
		{ID: "f6", Name: "x"},              // an ID of another form, from another helper
		{ID: "g7g7g7g7", Name: "frontend"}, // a name of 8 letters is a name
		{ID: "oldoldid", Name: "old"},      // an older helper's ID without a digit
		{ID: "h2h2h2h2", Name: "oldoldid"},
	}
	for word, want := range map[string]string{"abcd2345": "abcd2345", "zzzz2345": "zzzz2345", "web": "abcd2345", "dup": "d4d4d4d4", "f6": "f6", "x": "f6",
		"frontend": "g7g7g7g7", "oldoldid": "oldoldid"} {
		if s, err := findIn(nil, list, word, "box"); err != nil || s.ID != want {
			t.Errorf("%q finds %+v, %v, want %s", word, s, err, want)
		}
	}
	// An older ID without a digit matches first while its session exists.
	// Once that session is gone, a session named like it is reached.
	if s, err := findIn(nil, slices.DeleteFunc(slices.Clone(list), func(s client.Session) bool { return s.ID == "oldoldid" }), "oldoldid", "box"); err != nil || s.ID != "h2h2h2h2" {
		t.Errorf("an older ID without a digit, after its session went: %+v, %v", s, err)
	}
	_, err := findIn(nil, list, "qqqq2345", "box")
	text, code := reportOf(err)
	if code != 3 || !strings.Contains(text, `no session has the ID "qqqq2345" on box`) || !strings.Contains(text, `"box/c3c3c3c3" (qqqq2345)`) {
		t.Errorf("a word of an ID's form that only a name has: exit %d\n%s", code, text)
	}
	hosts := []client.Host{{Name: "box"}, {Name: "nuc"}}
	lists := map[string][]client.Session{"box": list, "nuc": {{ID: "n2n2n2n2", Name: "qqqq2345"}, {ID: "n3n3n3n3", Name: "frontend"}}}
	if m := matchesOf(hosts, lists, "qqqq2345"); len(m) != 0 {
		t.Errorf("a word of an ID's form matched names: %+v", m)
	}
	if m := matchesOf(hosts, lists, "abcd2345"); len(m) != 1 || m[0].s.ID != "abcd2345" {
		t.Errorf("a word of an ID's form matched %+v", m)
	}
	if m := matchesOf(hosts, lists, "frontend"); len(m) != 2 {
		t.Errorf("a name of 8 letters matched %+v", m)
	}
	text, code = reportOf(noSessionNamed("qqqq2345", lists))
	if code != 3 || !strings.Contains(text, `no session has the ID "qqqq2345" on any host`) ||
		!strings.Contains(text, `"box/c3c3c3c3" (qqqq2345)`) || !strings.Contains(text, `"nuc/n2n2n2n2" (qqqq2345)`) {
		t.Errorf("no session of that ID on any host: exit %d\n%s", code, text)
	}
	for _, s := range list {
		if addr := sessionObject("box", s)["address"]; addr != "box/"+s.ID {
			t.Errorf("%s/%q: the JSON address is %v", s.ID, s.Name, addr)
		}
	}
}

// A --json anywhere before "--" gets the JSON error line, also when the
// parser stops at a mistake before it or after it.
func TestJSONErrorWhereverJSONIs(t *testing.T) {
	hosttest.New(t)
	for _, args := range [][]string{
		{"version", "--bogus", "--json"},
		{"version", "--json", "--bogus"},
		{"--json", "version", "--bogus"},
		{"host", "alerts", "log", "box", "-n", "x", "--json"},
		{"host", "alerts", "log", "box", "--json", "-n", "x"},
		{"ls", "--json", "--host"},
		{"atach", "--json"},
	} {
		out, errOut, code := mainRun(t, args...)
		var line struct {
			V     int
			Error errorJSON
		}
		if code != 2 || out != "" || strings.Count(errOut, "\n") != 1 || json.Unmarshal([]byte(errOut), &line) != nil || line.Error.Kind != "usage" {
			t.Errorf("%s: exit %d\nstdout %q\nstderr %q", args, code, out, errOut)
		}
	}
	// After "--" it is a word for the session's command.
	if _, errOut, _ := mainRun(t, "new", "box", "--bogus", "--", "--json"); json.Valid([]byte(errOut)) {
		t.Errorf("a --json after -- turned JSON on: %q", errOut)
	}
	// When the flags parse, the last --json counts: here it is false, and
	// the error of the extra word is plain.
	if _, errOut, code := mainRun(t, "version", "extra", "--json", "--json=false"); code != 2 || json.Valid([]byte(errOut)) || !strings.Contains(errOut, "Error: ") {
		t.Errorf("version extra --json --json=false: exit %d, %q", code, errOut)
	}
	if _, errOut, code := mainRun(t, "version", "extra", "--json=false", "--json"); code != 2 || !json.Valid([]byte(errOut)) {
		t.Errorf("version extra --json=false --json: exit %d, %q", code, errOut)
	}
}

// new --detach --json needs no list of the host's sessions: its address
// is host/ID. A list that would fail changes nothing, and nothing warns.
func TestNewDetachJSONNeedsNoList(t *testing.T) {
	newTestHost(t)
	old := listSessions
	listSessions = func(context.Context, *client.Conn) ([]client.Session, error) {
		return nil, errors.New("the list broke")
	}
	defer func() { listSessions = old }()
	out, errOut, err := runApart(t, "new", "box", "--detach", "--json", "--wall", "none", "--downgrade", "--name", "api", "--", "sleep", "60")
	var s map[string]any
	if err != nil || json.Unmarshal([]byte(out), &s) != nil {
		t.Fatalf("%v\nstdout %q\nstderr %q", err, out, errOut)
	}
	if s["address"] != "box/"+s["id"].(string) || errOut != "" {
		t.Errorf("stdout %s\nstderr %q", out, errOut)
	}
}

// With --json, debug announces itself as a JSON line, whether --debug,
// --debug-file or the variable turns it on.
func TestDebugAnnouncementsAreJSON(t *testing.T) {
	hosttest.New(t)
	file := filepath.Join(t.TempDir(), "debug.jsonl")
	for _, c := range []struct {
		env  string
		args []string
	}{
		{"", []string{"--debug", "version", "--json"}},
		{"", []string{"--debug-file", file, "version", "--json"}},
		{file, []string{"version", "--json"}},
	} {
		t.Setenv(diag.EnvFile, c.env)
		out, errOut, code := mainRun(t, c.args...)
		var warn struct {
			V       int
			Warning map[string]string
		}
		if code != 0 || !json.Valid([]byte(out)) || json.Unmarshal([]byte(errOut), &warn) != nil || warn.Warning["code"] != "debug_on" {
			t.Errorf("%s with %s=%q: exit %d\nstdout %q\nstderr %q", c.args, diag.EnvFile, c.env, code, out, errOut)
		}
	}
	// Any warning that the client writes by itself becomes one JSON line.
	var b strings.Builder
	_, _ = jsonWarnings{w: &b, code: "debug_incomplete"}.Write([]byte("Warning: the debug file x is not complete.\nTry: make room.\n"))
	if b.String() != `{"v":1,"warning":{"code":"debug_incomplete","message":"Warning: the debug file x is not complete. Try: make room."}}`+"\n" {
		t.Errorf("jsonWarnings wrote %q", b.String())
	}
}

// Data goes to stdout, and every message to stderr, for each command that
// prints a line of its own when it has no data (cli-spec 7.5).
func TestStreamsOfEachCommand(t *testing.T) {
	hosttest.New(t)
	if out, errOut, err := runApart(t, "host", "ls"); err != nil || out != "" || !strings.HasPrefix(errOut, "No hosts yet.") {
		t.Errorf("host ls with no hosts: %v\nstdout %q\nstderr %q", err, out, errOut)
	}
	newTestHost(t)
	newSession(t, "api", "sleep", "60")
	for _, c := range []struct {
		args []string
		said string
	}{
		{[]string{"ports", "box/api"}, "api on box listens on no TCP ports.\n"},
		{[]string{"host", "restore", "box"}, "Nothing to restore on box.\n"},
		{[]string{"host", "alerts", "log", "box"}, "box has sent no alerts yet.\n"},
	} {
		out, errOut, err := runApart(t, c.args...)
		if err != nil || out != "" || errOut != c.said {
			t.Errorf("%s: %v\nstdout %q\nstderr %q", c.args, err, out, errOut)
		}
	}
	_ = os.MkdirAll(filepath.Join(client.ConfigDir(), "ssh"), 0o700)
	_ = os.WriteFile(filepath.Join(client.ConfigDir(), "ssh", "config-gone"), []byte("# generated\n"), 0o600)
	out, errOut, err := runApart(t, "doctor")
	if err != nil || !strings.Contains(out, "This computer:") || !strings.Contains(out, "✕ 1 generated SSH config") || strings.Contains(out, "Try:") ||
		!strings.Contains(errOut, "Try: ") {
		t.Errorf("doctor: %v\nstdout %q\nstderr %q", err, out, errOut)
	}
	// A failed check's own Try: line is a message too: with no ssh on
	// PATH, the row is on stdout and its Try: line on stderr.
	t.Setenv("PATH", t.TempDir())
	out, errOut, err = runApart(t, "doctor")
	if err != nil || !strings.Contains(out, "✕ ssh") || strings.Contains(out, "Try:") || !strings.Contains(errOut, "Try: install OpenSSH") {
		t.Errorf("doctor with a failed check: %v\nstdout %q\nstderr %q", err, out, errOut)
	}
}

// --fix --json needs -y, which answers for every fix: with it, the fixes
// run, and the JSON holds how each ended.
func TestFixWithJSON(t *testing.T) {
	hosttest.New(t)
	gone := filepath.Join(client.ConfigDir(), "ssh", "config-gone")
	_ = os.MkdirAll(filepath.Dir(gone), 0o700)
	_ = os.WriteFile(gone, []byte("# generated\n"), 0o600)
	_, errOut, code := mainRun(t, "doctor", "--fix", "--json")
	if code != 4 || !strings.Contains(errOut, `"try":"`) || !strings.Contains(errOut, `doctor --fix --json -y"`) {
		t.Errorf("--fix --json: exit %d, %q", code, errOut)
	}
	out, errOut, code := mainRun(t, "doctor", "--fix", "--json", "-y")
	var d struct {
		V     int
		Fixes []struct {
			ID   string
			Done bool
		}
	}
	if code != 0 || errOut != "" || json.Unmarshal([]byte(out), &d) != nil || len(d.Fixes) != 1 || !d.Fixes[0].Done {
		t.Errorf("--fix --json -y: exit %d\nstdout %q\nstderr %q", code, out, errOut)
	}
	if _, err := os.Stat(gone); err == nil {
		t.Error("the fix did not run")
	}
}

// A secret's command gets the terminal when input is not turned off, and
// stdin and stderr are terminals, also when stdout goes to a file.
func TestSecretTerminal(t *testing.T) {
	t.Setenv(noInputEnv, "")
	term := hosttest.NewTerminal(t, 80, 10)
	_, pw, _ := os.Pipe()
	oldIn, oldOut, oldErr := os.Stdin, os.Stdout, os.Stderr
	defer func() { os.Stdin, os.Stdout, os.Stderr = oldIn, oldOut, oldErr }()
	os.Stdin, os.Stdout, os.Stderr = term.TTY, pw, term.TTY
	root := Root()
	cmd, _, _ := root.Find([]string{"new"})
	if !secretTerminal(cmd) {
		t.Error("no terminal for a secret, with stdout in a file")
	}
	_ = cmd.ParseFlags([]string{"--no-input"})
	if secretTerminal(cmd) {
		t.Error("a terminal for a secret, with --no-input")
	}
	cmd, _, _ = Root().Find([]string{"new"})
	os.Stdin = pw
	if secretTerminal(cmd) {
		t.Error("a terminal for a secret, with stdin from a pipe")
	}
}
