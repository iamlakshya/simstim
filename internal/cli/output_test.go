package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
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

// With --json, a failure writes nothing to stdout, and one JSON line to
// stderr (question C-7).
func TestJSONErrorIsOneLine(t *testing.T) {
	newTestHome(t)
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
	newTestHome(t)
	fakeBox(t, nil)
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
var flagValueCases = []struct {
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
}

func TestAFlagsValueIsNoFlag(t *testing.T) {
	for _, c := range flagValueCases {
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
	}
}
func TestNoColourFlagAndPipe(t *testing.T) {
	newTestHome(t)
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	r, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer pw.Close()
	if colourOn(pw) {
		t.Error("colour on a stream that is not a terminal")
	}
	// --no-color sets NO_COLOR for the run, so the launcher follows it too.
	t.Setenv("NO_COLOR", "")
	if _, err := run(t, "--no-color", "config", "path"); err != nil || os.Getenv("NO_COLOR") == "" {
		t.Errorf("--no-color: %v, NO_COLOR=%q", err, os.Getenv("NO_COLOR"))
	}
}
