//go:build unix

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

// Each JSON shape of cli-spec 7.1, from the real commands against the test
// host, against a golden file. The values that change from run to run are
// masked; every key, and the shape around it, is the command's own.
func TestGoldenJSON(t *testing.T) {
	th := newTestHost(t)
	fakeClaude(t, th) // for agents login
	// Fix both starts to the same cap, even on systems without /proc.
	if err := os.WriteFile(filepath.Join(client.ConfigDir(), "config.toml"), []byte("[sessions]\nmemory_max = '256M'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	show := func(args ...string) {
		out, errOut, err := runApart(t, args...)
		if err != nil {
			t.Fatalf("%s: %v\n%s", args, err, errOut)
		}
		if args[0] == "new" || args[0] == "agents" {
			var row client.Session
			if err := json.Unmarshal([]byte(out), &row); err != nil {
				t.Fatal(err)
			}
			if row.MemoryMax != 256<<20 {
				t.Fatalf("fixture cap = %d, want 256 MiB", row.MemoryMax)
			}
		}
		// An address that uses the ID shows <id> in its place.
		if m := regexp.MustCompile(`(?m)^\s*"id": "([a-z0-9]+)",?$`).FindStringSubmatch(out); m != nil {
			out = strings.ReplaceAll(out, `"box/`+m[1]+`"`, `"box/<id>"`)
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
	show("agents", "login", "claude", "box", "--json")

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
func TestFlagValuesKeepTerminalColour(t *testing.T) {
	term := hosttest.NewTerminal(t, 80, 10)
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
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
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pr.Close()
	defer pw.Close()
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
