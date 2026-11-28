package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/tui"
)

// Every entry in the launcher's palette registry has either a real CLI command
// path or a launcher-only reason, never neither and never both
// (docs/contributing/cli-spec.md 15.3).
func TestPaletteMatchesCLI(t *testing.T) {
	root := Root()
	entries := tui.PaletteEntries()
	if len(entries) < 20 {
		t.Fatalf("only %d palette entries", len(entries))
	}
	foundPeek := false
	for _, e := range entries {
		if strings.Contains(strings.ToLower(e.Name), "preview") {
			t.Errorf("palette entry %q still uses retired word preview", e.Name)
		}
		if e.Name == "session: peek" {
			foundPeek = true
		}
		if err := checkPaletteEntry(root, e); err != "" {
			t.Errorf("palette entry %q: %s", e.Name, err)
		}
	}
	if !foundPeek {
		t.Error("palette registry is missing session: peek")
	}

	// Mutants: verify the checker rejects each failure mode.
	for _, bad := range []tui.PaletteEntry{
		{Name: "neither"},
		{Name: "both", CLI: "ls", LauncherOnly: "redundant"},
		{Name: "missing cmd", CLI: "host nonexistent"},
		{Name: "hidden cmd", CLI: "kill"},
		{Name: "missing flag", CLI: "show --no-such-flag"},
	} {
		if err := checkPaletteEntry(root, bad); err == "" {
			t.Errorf("mutant %+v unexpectedly passed checkPaletteEntry", bad)
		}
	}
}

func checkPaletteEntry(root *cobra.Command, e tui.PaletteEntry) string {
	hasCLI := strings.TrimSpace(e.CLI) != ""
	hasOnly := strings.TrimSpace(e.LauncherOnly) != ""
	switch {
	case !hasCLI && !hasOnly:
		return "has neither cli nor launcherOnly"
	case hasCLI && hasOnly:
		return "has both cli and launcherOnly"
	case !hasCLI:
		return ""
	}
	fields := strings.Fields(e.CLI)
	var cmdWords, flags []string
	for _, w := range fields {
		if strings.HasPrefix(w, "-") {
			flags = append(flags, w)
			continue
		}
		if len(flags) > 0 {
			return "has positional word after flag in " + e.CLI
		}
		cmdWords = append(cmdWords, w)
	}
	c, rest, err := root.Find(cmdWords)
	if err != nil {
		return "unknown command " + e.CLI + ": " + err.Error()
	}
	if len(rest) != 0 || shortPath(c) != strings.Join(cmdWords, " ") {
		return "command path " + e.CLI + " does not match " + shortPath(c)
	}
	if c.Hidden || !c.Runnable() {
		return "command " + e.CLI + " is hidden or not runnable"
	}
	for _, fl := range flags {
		name := strings.TrimLeft(fl, "-")
		f := c.Flags().Lookup(name)
		if f == nil || f.Hidden {
			return "command " + shortPath(c) + " has no visible flag " + fl
		}
	}
	return ""
}

// Every non-hidden command of Root() has a row in the UX spec's CLI table
// (docs/contributing/cli-spec.md 15.3).
func TestUXSpecListsEveryCommand(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "contributing", "ux-spec.md"))
	if err != nil {
		t.Fatalf("read ux-spec.md: %v", err)
	}
	rows := parseUXSpecCLITable(t, string(raw))
	if len(rows) < 30 {
		t.Fatalf("parsed only %d rows from ux-spec.md §6", len(rows))
	}
	eachCommand(Root(), func(c *cobra.Command) {
		if c.Hidden || IsTopic(c) {
			return
		}
		path := shortPath(c)
		if !rows[path] {
			t.Errorf("command %q (%s) has no row in docs/contributing/ux-spec.md §6 CLI table", path, c.CommandPath())
		}
	})
}

func parseUXSpecCLITable(t *testing.T, doc string) map[string]bool {
	t.Helper()
	_, after, ok := strings.Cut(doc, "\n## 6. CLI\n")
	if !ok {
		t.Fatal("docs/contributing/ux-spec.md has no '## 6. CLI' section")
	}
	if next := strings.Index(after, "\n## "); next >= 0 {
		after = after[:next]
	}
	rows := map[string]bool{}
	inTable := false
	for _, line := range strings.Split(after, "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "| Command |") {
			inTable = true
			continue
		}
		if !inTable {
			continue
		}
		if !strings.HasPrefix(trim, "|") {
			break
		}
		if strings.HasPrefix(trim, "| ---") {
			continue
		}
		parts := strings.SplitN(trim, "|", 3)
		if len(parts) < 3 {
			continue
		}
		cell := strings.TrimSpace(parts[1])
		cell = strings.Trim(cell, "`")
		words := strings.Fields(cell)
		if len(words) == 0 || words[0] != brand.Name {
			continue
		}
		var path []string
		for _, w := range words[1:] {
			if strings.HasPrefix(w, "[") || strings.HasPrefix(w, "<") || strings.HasPrefix(w, "-") || strings.Contains(w, "|") {
				break
			}
			path = append(path, w)
		}
		key := strings.Join(path, " ")
		if rows[key] {
			t.Errorf("ux-spec.md §6 lists command %q more than once", key)
		}
		rows[key] = true
	}
	return rows
}

// show --screen prints the session's last screen as plain text (one line per
// row, no escape sequences, no trailing spaces), and --screen --json adds a
// screen array to the session object (docs/contributing/cli-spec.md 9.6).
func TestShowScreen(t *testing.T) {
	newTestHost(t)
	newSession(t, "scr", "/bin/sh", "-c", `printf '\033[32mhello screen\033[0m   \r\nsecond line\r\n'; exec sleep 60`)

	var out string
	hosttest.WaitFor(t, 10*time.Second, "screen output", func() bool {
		got, errOut, err := runApart(t, "show", "box/scr", "--screen")
		if err != nil || errOut != "" {
			return false
		}
		out = got
		return strings.Contains(out, "hello screen") && strings.Contains(out, "second line")
	})

	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("show --screen lines = %q", lines)
	}
	for i, l := range lines {
		if strings.ContainsRune(l, '\x1b') {
			t.Errorf("line %d has escape sequence: %q", i, l)
		}
		if strings.TrimRight(l, " \t") != l {
			t.Errorf("line %d has trailing whitespace: %q", i, l)
		}
	}
	if lines[0] != "hello screen" || lines[1] != "second line" {
		t.Errorf("first two lines = %q, want [hello screen, second line]", lines[:2])
	}

	rawJSON := okJSON(t, "show", "box/scr", "--screen", "--json")
	var obj struct {
		V       int      `json:"v"`
		Name    string   `json:"name"`
		Address string   `json:"address"`
		Screen  []string `json:"screen"`
	}
	if err := json.Unmarshal([]byte(rawJSON), &obj); err != nil {
		t.Fatalf("decode JSON: %v\n%s", err, rawJSON)
	}
	if obj.V != 1 || obj.Name != "scr" || !strings.HasPrefix(obj.Address, "box/") {
		t.Errorf("unexpected session JSON: %+v", obj)
	}
	if len(obj.Screen) < 2 || obj.Screen[0] != "hello screen" || obj.Screen[1] != "second line" {
		t.Errorf("JSON screen = %q", obj.Screen)
	}
}

// host test --reconnect closes the shared SSH ControlMaster via ssh -O exit
// before opening and testing a fresh connection (docs/contributing/cli-spec.md 15.2 & 15.4).
func TestHostTestReconnectWithFakeSSHMaster(t *testing.T) {
	th := newTestHost(t)
	sshLog := filepath.Join(th.h.Home, "ssh.log")

	// Plain host test does not close the shared SSH master.
	out := ok(t, "host", "test", "box")
	if !strings.Contains(out, "box answers:") {
		t.Fatalf("host test output = %q", out)
	}
	if b, _ := os.ReadFile(sshLog); strings.Contains(string(b), " -O exit ") {
		t.Errorf("plain host test unexpectedly ran ssh -O exit:\n%s", b)
	}

	// host test --reconnect runs ssh -O exit box before testing the connection.
	_ = os.Remove(sshLog)
	out = ok(t, "host", "test", "box", "--reconnect")
	if !strings.Contains(out, "box answers:") {
		t.Fatalf("host test --reconnect output = %q", out)
	}
	logBytes, err := os.ReadFile(sshLog)
	if err != nil {
		t.Fatalf("read ssh.log: %v", err)
	}
	if !strings.Contains(string(logBytes), "-O exit box") {
		t.Errorf("host test --reconnect did not close SSH master with -O exit:\n%s", logBytes)
	}

	// JSON output works with --reconnect too.
	_ = os.Remove(sshLog)
	jout := okJSON(t, "host", "test", "box", "--reconnect", "--json")
	var rep struct {
		V    int    `json:"v"`
		Host string `json:"host"`
	}
	if err := json.Unmarshal([]byte(jout), &rep); err != nil || rep.V != 1 || rep.Host != "box" {
		t.Errorf("host test --reconnect --json = %q (%v)", jout, err)
	}
	if b, _ := os.ReadFile(sshLog); !strings.Contains(string(b), "-O exit box") {
		t.Errorf("host test --reconnect --json did not close SSH master:\n%s", b)
	}
}

// config keymap accepts retired "session: preview", applies it to "session: peek",
// and warns once on stderr (plain and JSON).
func TestConfigKeymapOldPreviewWarns(t *testing.T) {
	hosttest.New(t)
	t.Setenv("NO_COLOR", "1")
	writeConfigFiles(t, "[keymap]\n\"session: preview\" = \"p\"\n", "")

	out, errOut, err := runApart(t, "config", "keymap")
	if err != nil {
		t.Fatalf("config keymap: %v\nstderr: %s", err, errOut)
	}
	if !strings.Contains(out, "session: peek") || !strings.Contains(out, " p\n") {
		t.Errorf("config keymap output did not bind session: peek to p:\n%s", out)
	}
	if strings.Contains(out, "session: preview") {
		t.Errorf("config keymap still lists retired session: preview:\n%s", out)
	}
	if strings.Count(errOut, "Warning: ") != 1 || !strings.Contains(errOut, `"session: preview" is now "session: peek"`) {
		t.Errorf("config keymap stderr = %q", errOut)
	}

	out, errOut, err = runApart(t, "config", "keymap", "--json")
	if err != nil {
		t.Fatalf("config keymap --json: %v\nstderr: %s", err, errOut)
	}
	if !json.Valid([]byte(out)) {
		t.Errorf("invalid JSON stdout: %q", out)
	}
	var warn struct {
		V       int               `json:"v"`
		Warning map[string]string `json:"warning"`
	}
	if err := json.Unmarshal([]byte(errOut), &warn); err != nil || warn.V != 1 || warn.Warning["old"] != "session: preview" || warn.Warning["new"] != "session: peek" {
		t.Errorf("config keymap --json stderr = %q (%v)", errOut, err)
	}
}
