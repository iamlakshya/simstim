package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/client/clienttest"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
)

func TestVersioned(t *testing.T) {
	for in, want := range map[string]string{
		`{"a":1}`:        "{\n  \"v\": 1,\n  \"a\": 1\n}\n",
		`{}`:             "{\n  \"v\": 1\n}\n",
		`[{"a":1},{}]`:   "[\n  {\n    \"v\": 1,\n    \"a\": 1\n  },\n  {\n    \"v\": 1\n  }\n]\n",
		`[]`:             "[]\n",
		`null`:           "null\n",
		`["x",{"b":[]}]`: "[\n  \"x\",\n  {\n    \"v\": 1,\n    \"b\": []\n  }\n]\n",
	} {
		got, err := versioned(json.RawMessage(in))
		if err != nil || string(got) != want {
			t.Errorf("versioned(%s) = %q, %v, want %q", in, got, err, want)
		}
	}
}

// volatile are the keys whose values change from run to run, or from
// machine to machine: a golden file shows them as <key>.
var volatile = regexp.MustCompile(`(?m)^(\s*)"(id|ID|cwd|created_at|since|last_activity_at|last_attached_at|host_version|helper|connect_ms|round_trip_ms|detail|state|source|memory_max|memory_current|unit|version)": ("[^"]*"|[0-9.]+)(,?)$`)

// addressKey is the key of an address: host/ID, whose ID is volatile.
var addressKey = regexp.MustCompile(`(?m)^(\s*"address": "[^"/]*/)[^"]*"`)

// masked is a command's real JSON with its volatile values replaced.
func masked(out string) string {
	return addressKey.ReplaceAllString(volatile.ReplaceAllString(out, `$1"$2": "<$2>"$4`), `$1<id>"`)
}

// Each JSON shape of cli-spec 7.1, from the real commands against the test
// host, against a golden file. The values that change from run to run are
// masked; every key, and the shape around it, is the command's own.
func TestGoldenJSON(t *testing.T) {
	newTestHost(t)
	var b strings.Builder
	show := func(args ...string) {
		out, errOut, err := runApart(t, args...)
		if err != nil {
			t.Fatalf("%s: %v\n%s", args, err, errOut)
		}
		fmt.Fprintf(&b, "== %s\n%s", strings.Join(args, " "), masked(out))
	}
	// An explicit cap keeps the JSON shape the same with and without /proc.
	show("new", "box", "--detach", "--json", "--name", "api", "--memory-max", "256M", "--", "sleep", "60")
	show("ls", "--json")
	show("ports", "box/api", "--json")
	show("show", "box/api", "--json")
	show("doctor", "--json")
	show("host", "ls", "--json")
	show("host", "test", "box", "--json")
	show("host", "restore", "box", "--json")
	show("host", "status", "box", "--json")
	show("version", "--json")
	show("config", "keymap", "--json")

	// From fixed data: the details of a host that answered and of one that
	// did not, and what host upgrade did.
	fixed := func(name string, v any) {
		out, err := versioned(v)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "== %s\n%s", name, out)
	}
	d := clienttest.HostDetails()
	fixed("host show box --json", hostShowObject(d, nil))
	fixed("host show box --json, when box does not answer", hostShowObject(client.HostDetails{Host: d.Host, Tier: d.Tier}, errors.New("cannot reach box")))
	fixed("host upgrade box --json", upgradeResult{Host: "box", Helper: "0.1.2-1a2b3c4d", Outcome: upgradeInstalled})

	fmt.Fprintf(&b, "== errors\n")
	for _, args := range [][]string{{"host", "test", "nowhere", "--json"}, {"atach", "box/api", "--json"}, {"new", "box", "--json", "--", "htop"}} {
		_, errOut, _ := mainRun(t, args...)
		b.WriteString(errOut)
	}

	got := strings.ReplaceAll(b.String(), brand.Name, "<name>")
	golden := filepath.Join("testdata", "json.golden")
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
		t.Errorf("the JSON differs from %s (run go test ./internal/cli -run TestGoldenJSON -update, and read the diff):\n%s", golden, got)
	}
}

var keyRE = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// checkJSON checks one command's JSON: one value with one newline, "v": 1
// in each top-level object, keys in snake_case and no null. Maps that hold
// the user's data keep their own keys, and old allows the Go keys of host
// ls until their end.
func checkJSON(t *testing.T, name, out string, old bool) {
	t.Helper()
	if !strings.HasSuffix(out, "}\n") && !strings.HasSuffix(out, "]\n") || strings.Count(strings.TrimSpace(out), "\n{") > 0 && !strings.HasPrefix(out, "[") {
		t.Errorf("%s: not one value with one newline:\n%s", name, out)
	}
	var v any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("%s: %v\n%s", name, err, out)
	}
	tops := []any{v}
	if arr, ok := v.([]any); ok {
		tops = arr
	}
	for _, top := range tops {
		if obj, ok := top.(map[string]any); ok && obj["v"] != float64(1) {
			t.Errorf("%s: a top-level object without \"v\": 1: %v", name, obj)
		}
	}
	data := map[string]bool{"labels": true, "agents": true, "agent_versions": true, "env": true}
	var walk func(path string, x any)
	walk = func(path string, x any) {
		switch x := x.(type) {
		case nil:
			if !old {
				t.Errorf("%s: null at %s", name, path)
			}
		case map[string]any:
			for k, val := range x {
				goKey := old && k[0] >= 'A' && k[0] <= 'Z' // the Go keys of host ls, as before
				if !keyRE.MatchString(k) && !goKey {
					t.Errorf("%s: the key %q at %s is not snake_case", name, k, path)
				}
				if !data[k] && !goKey {
					walk(path+"."+k, val)
				}
			}
		case []any:
			for i, val := range x {
				walk(fmt.Sprintf("%s[%d]", path, i), val)
			}
		}
	}
	walk("", v)
}

// Each command with --json against the test host: one value on stdout,
// with "v" and snake_case keys, and nothing on stderr but JSON lines.
func TestCommandsPrintJSON(t *testing.T) {
	newTestHost(t)
	newSession(t, "web", "/bin/sh")
	ended := newSession(t, "done", "/bin/sh", "-c", "exit 0")
	agentEnded(t, ended)
	for _, args := range [][]string{
		{"ls", "--json"},
		{"resume", "box/done", "--detach", "--json"},
		{"ports", "box/web", "--json"},
		{"doctor", "--json"},
		{"host", "ls", "--json"},
		{"host", "show", "box", "--json"},
		{"host", "status", "box", "--json"},
		{"host", "test", "box", "--json"},
		{"host", "doctor", "box", "--json"},
		{"host", "restore", "box", "--json"},
		{"host", "alerts", "box", "--json"},
		{"host", "alerts", "log", "box", "--json"},
		{"agents", "ls", "--json"},
		{"config", "keymap", "--json"},
		{"version", "--json"},
	} {
		out, errOut, err := runApart(t, args...)
		if err != nil {
			t.Errorf("%s: %v\n%s", args, err, errOut)
			continue
		}
		checkJSON(t, strings.Join(args, " "), out, args[0] == "host" && args[1] == "ls")
		for _, l := range strings.Split(strings.TrimSpace(errOut), "\n") {
			if l != "" && !json.Valid([]byte(l)) {
				t.Errorf("%s: stderr has a line that is not JSON: %q", args, l)
			}
		}
	}
	// Both forms of the keys of host ls, until their end, and a warning.
	out, errOut, _ := runApart(t, "host", "ls", "--json")
	for _, k := range []string{`"Name": "box"`, `"name": "box"`, `"SSH": "box"`, `"ssh": "box"`} {
		if !strings.Contains(out, k) {
			t.Errorf("host ls --json lacks %s:\n%s", k, out)
		}
	}
	if !strings.Contains(errOut, `"warning":{"code":"old_keys"`) || strings.Count(errOut, "\n") != 1 {
		t.Errorf("host ls --json warned %q", errOut)
	}
}

// The first acceptance line of C4: `new box --detach --json` prints one
// JSON object with id, name, host and "v": 1, and nothing else on stdout.
func TestNewDetachJSON(t *testing.T) {
	newTestHost(t)
	out, errOut, err := runApart(t, "new", "box", "--detach", "--json", "--name", "api", "--", "sleep", "60")
	if err != nil {
		t.Fatalf("%v\n%s", err, errOut)
	}
	var s map[string]any
	if err := json.Unmarshal([]byte(out), &s); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if s["v"] != float64(1) || s["host"] != "box" || s["name"] != "api" || s["id"] == "" || s["address"] != "box/"+s["id"].(string) || errOut != "" {
		t.Fatalf("stdout %s, stderr %q", out, errOut)
	}
	checkJSON(t, "new --detach --json", out, false)

	// A second session of that name gets its ID in its address too.
	out = okJSON(t, "new", "box", "--detach", "--json", "--name", "api", "--", "sleep", "60")
	_ = json.Unmarshal([]byte(out), &s)
	if s["address"] != "box/"+s["id"].(string) {
		t.Errorf("the second api's address is %v", s["address"])
	}

	// --json without --detach is a usage error.
	got, code := lineErr(t, "new", "box", "--json")
	if code != 2 || !strings.Contains(got, "--json needs --detach") || !strings.Contains(got, "Try: "+brand.Name+" new box --json --detach") {
		t.Errorf("new --json: exit %d\n%s", code, got)
	}
}

// With --json, a failure writes nothing to stdout, and one JSON line to
// stderr (question C-7).
func TestJSONErrorIsOneLine(t *testing.T) {
	hosttest.New(t)
	old := os.Args
	defer func() { os.Args = old }()
	for _, c := range []struct {
		args []string
		code int
		kind string
	}{
		{[]string{"host", "test", "nowhere", "--json"}, 3, "not_found"},
		{[]string{"atach", "x", "--json"}, 2, "usage"},
		{[]string{"ls", "--json"}, 1, "failed"},
		{[]string{"new", "box", "--detach", "--json"}, 3, "not_found"},
		{[]string{"new", "box", "--json"}, 2, "usage"},
		{[]string{"doctor", "--fix", "--json"}, 4, "needs_answer"},
	} {
		os.Args = append([]string{"client"}, c.args...)
		var code int
		out, errOut := captureOutput(t, func() { code = Main() })
		var line struct {
			V     int
			Error errorJSON
		}
		if code != c.code || out != "" || strings.Count(errOut, "\n") != 1 || json.Unmarshal([]byte(errOut), &line) != nil ||
			line.V != 1 || line.Error.Kind != c.kind || line.Error.Exit != c.code || line.Error.Try == "" {
			t.Errorf("%s: exit %d, stdout %q, stderr %q", c.args, code, out, errOut)
		}
	}
}

// Output to a pipe has no escape byte and no spinner: not in data, not in
// a warning and not in an error.
func TestPipedOutputHasNoColour(t *testing.T) {
	newTestHost(t)
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	old := os.Args
	defer func() { os.Args = old }()
	for _, args := range [][]string{{"ls"}, {"host", "ls", "--json"}, {"host", "test", "nowhere"}, {"doctor"}, {"version"}} {
		os.Args = append([]string{"client"}, args...)
		out, errOut := captureOutput(t, func() { Main() })
		if strings.ContainsRune(out+errOut, 0x1b) || strings.ContainsAny(out+errOut, "⠁⠂⠄⡀⢀⠠⠐⠈") {
			t.Errorf("%s printed an escape or a spinner: %q %q", args, out, errOut)
		}
	}
}

// A word that is a flag's value is no flag: in "--debug-file --json",
// --json names the debug file. So when the line does not parse, its error
// is no JSON for that word, and a --no-color there leaves colour on.
func TestAFlagsValueIsNoFlag(t *testing.T) {
	term := hosttest.NewTerminal(t, 80, 10)
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	for _, c := range []struct {
		args         []string
		json, colour bool
	}{
		{[]string{"version", "--debug-file", "--json", "--bogus"}, false, true},
		{[]string{"version", "--debug-file", "--no-color", "--bogus"}, false, true},
		{[]string{"--debug-file", "--json", "version", "--bogus"}, false, true},
		{[]string{"version", "--debug-file=x", "--json", "--bogus"}, true, true},
		{[]string{"version", "--no-color", "--bogus"}, false, false},
		{[]string{"version", "--bogus", "--", "--json"}, false, true},
		// A flag of the command that failed takes its value too.
		{[]string{"new", "box", "--dir", "--json", "--bogus"}, false, true},
		{[]string{"new", "box", "--dir", "--no-color", "--bogus"}, false, true},
		// A bool flag's value counts, and the last one wins.
		{[]string{"version", "--json=1", "--bogus"}, true, true},
		{[]string{"version", "--json", "--json=false", "--bogus"}, false, true},
		{[]string{"version", "--no-color", "--no-color=false", "--bogus"}, false, true},
	} {
		root := Root()
		root.SetArgs(c.args)
		var cmd *cobra.Command
		var err error
		_, _ = captureOutput(t, func() { cmd, err = root.ExecuteC() })
		if err == nil {
			t.Fatalf("%q parsed", c.args)
		}
		if got := jsonAsked(cmd, c.args); got != c.json {
			t.Errorf("%q: JSON %v, want %v", c.args, got, c.json)
		}
		if got := colourAsked(cmd, c.args, term.TTY); got != c.colour {
			t.Errorf("%q: colour %v, want %v", c.args, got, c.colour)
		}
	}
}

// Colour follows NO_COLOR, TERM=dumb and --no-color, for each stream.
func TestColourRules(t *testing.T) {
	term := hosttest.NewTerminal(t, 80, 10)
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	if !colourAsked(nil, nil, term.TTY) {
		t.Error("no colour on a terminal")
	}
	_, pw, _ := os.Pipe()
	if colourAsked(nil, []string{"ls", "--no-color"}, term.TTY) || colourOn(pw) {
		t.Error("colour with --no-color, or on a stream that is not a terminal")
	}
	if colourAsked(nil, []string{"--", "--no-color"}, term.TTY) != true {
		t.Error("a --no-color after -- turned colour off")
	}
	t.Setenv("TERM", "dumb")
	if colourOn(term.TTY) {
		t.Error("colour with TERM=dumb")
	}
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "1")
	if colourOn(term.TTY) {
		t.Error("colour with NO_COLOR")
	}
	// --no-color sets NO_COLOR for the run, so the launcher follows it too.
	t.Setenv("NO_COLOR", "")
	if _, err := run(t, "--no-color", "config", "path"); err != nil || os.Getenv("NO_COLOR") == "" {
		t.Errorf("--no-color: %v, NO_COLOR=%q", err, os.Getenv("NO_COLOR"))
	}
}

// Confirmations go to stderr, and data to stdout (cli-spec 7.5).
func TestStreams(t *testing.T) {
	newTestHost(t)
	out, errOut, err := runApart(t, "new", "box", "--detach", "--name", "s", "--", "sleep", "60")
	if err != nil || out != "" || !strings.HasPrefix(errOut, "Started box/") {
		t.Errorf("new --detach: %v, stdout %q, stderr %q", err, out, errOut)
	}
	out, errOut, err = runApart(t, "ls")
	if err != nil || !strings.Contains(out, " s ") || errOut != "" {
		t.Errorf("ls: %v, stdout %q, stderr %q", err, out, errOut)
	}
	out, errOut, err = runApart(t, "rm", "box/s", "-y")
	if err != nil || out != "" || !strings.HasPrefix(errOut, "Deleted s (") {
		t.Errorf("rm -y: %v, stdout %q, stderr %q", err, out, errOut)
	}
	var ue *client.UserError
	if _, _, err := runApart(t, "resume", "box/x", "--json"); !errors.As(err, &ue) || ue.Code != "json_needs_detach" {
		t.Errorf("resume --json without --detach: %v", err)
	}
}
