package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
)

// warningOf is the warning of the old form id, without colour, with old
// and now in place of the row's forms when they are set.
func warningOf(t *testing.T, id, old, now string) string {
	t.Helper()
	o, ok := findOld(id)
	if !ok {
		t.Fatalf("no row %q", id)
	}
	typed := *o
	if old != "" {
		typed.Old, typed.New = old, now
	}
	return oldWarning(&typed, false) + "\n"
}

// Each old form of task C6 works as it did, and warns once on stderr
// (docs/contributing/cli-spec.md 3.3).
func TestEachOldFormWorksAndWarnsOnce(t *testing.T) {
	got := withRecorder(t)
	t.Setenv("NO_COLOR", "1")
	// The old form sets what the new form sets.
	for _, c := range []struct {
		id        string
		old, now  []string
		flag      string
		wantValue string
	}{
		{"-v", []string{"-v"}, []string{"--version"}, "version", "true"},
		{"new -d", []string{"new", "box", "-d", "/srv/app"}, []string{"new", "box", "-C", "/srv/app"}, "dir", "/srv/app"},
		{"new --setup", []string{"new", "box", "--setup"}, []string{"new", "box", "--setup-script"}, "setup-script", "true"},
		{"new --no-setup", []string{"new", "box", "--no-setup"}, []string{"new", "box", "--no-setup-script"}, "no-setup-script", "true"},
		{"agents install --force", []string{"agents", "install", "claude", "--force"}, []string{"agents", "install", "claude", "--reinstall"}, "reinstall", "true"},
		{"host alerts set --token-cmd", []string{"host", "alerts", "set", "box", "--token-cmd", "echo x"}, []string{"host", "alerts", "set", "box", "--token-command", "echo x"}, "token-command", "echo x"},
		{"host alerts log -n", []string{"host", "alerts", "log", "box", "-n", "5"}, []string{"host", "alerts", "log", "box", "--lines", "5"}, "lines", "5"},
	} {
		if v := parsed(t, c.flag, c.old...).Value.String(); v != c.wantValue {
			t.Errorf("%s: --%s = %q, want %q", c.id, c.flag, v, c.wantValue)
		}
		if v := parsed(t, c.flag, c.now...).Value.String(); v != c.wantValue {
			t.Errorf("%s, the new form: --%s = %q, want %q", c.id, c.flag, v, c.wantValue)
		}
	}
	for _, line := range [][]string{{"--memory", "8"}, {"--memory", "8G"}, {"--disk", "8"}, {"--disk", "8GiB"}} {
		if v := parsed(t, line[0][2:], append([]string{"host", "create", "rec"}, line...)...).Value.(*sizeValue); v.bytes != 8<<30 {
			t.Errorf("host create %s = %d bytes", line, v.bytes)
		}
	}
	// A run warns once, before it fails or ends, and only about what it used.
	for _, c := range []struct {
		id       string
		line     []string
		code     int
		old, now string
	}{
		{"-v", []string{"-v"}, 0, "", ""},
		{"new -d", []string{"new", "nowhere", "-d", "/a", "-d", "/b", "--detach"}, 3, "", ""},
		{"new --setup", []string{"new", "nowhere", "--setup", "--setup", "--detach"}, 3, "", ""},
		{"new --no-setup", []string{"new", "nowhere", "--no-setup", "--detach"}, 3, "", ""},
		{"agents install --force", []string{"agents", "install", "claude", "--force", "--host", "nowhere"}, 3, "", ""},
		{"host alerts set --token-cmd", []string{"host", "alerts", "set", "nowhere", "--token-cmd", "echo x"}, 2, "", ""},
		{"host alerts log -n", []string{"host", "alerts", "log", "nowhere", "-n", "5"}, 3, "", ""},
		{"host create --memory", []string{"host", "create", "rec", "--memory", "8"}, 1, "--memory 8", "--memory 8G"},
		{"host create --disk", []string{"host", "create", "rec", "--disk", "1.5"}, 1, "--disk 1.5", "--disk 1.5G"},
	} {
		out, stderr, code := mainLine(t, c.line...)
		want := warningOf(t, c.id, c.old, c.now)
		if code != c.code || !strings.HasPrefix(stderr, want) || strings.Count(stderr, "Warning:") != 1 {
			t.Errorf("%s: exit %d, want %d, and stderr\n%s\nwant it to start with\n%s", strings.Join(c.line, " "), code, c.code, stderr, want)
		}
		if strings.Contains(out, "Warning") {
			t.Errorf("%s: a warning on stdout: %q", strings.Join(c.line, " "), out)
		}
	}
	if out, _, _ := mainLine(t, "-v"); !strings.Contains(out, "(protocol ") {
		t.Errorf("-v printed %q", out)
	}
	if _, stderr, _ := mainLine(t, "host", "alerts", "set", "nowhere", "--token-cmd", "echo x"); !strings.Contains(stderr, "Error: --token-command needs --ntfy.") {
		t.Errorf("--token-cmd did not set --token-command:\n%s", stderr)
	}
	if _, stderr, code := mainLine(t, "host", "alerts", "log", "nowhere", "-n", "x"); code != 2 || !strings.Contains(stderr, `Error: -n "x" is not a whole number.`) {
		t.Errorf("-n x = %d:\n%s", code, stderr)
	}
	if _, _, _ = mainLine(t, "host", "create", "rec", "--memory", "8", "--disk", "40"); got.Memory != 8<<30 || got.Disk != 40<<30 {
		t.Errorf("--memory 8 --disk 40 gave %+v", *got)
	}
	// Two old forms warn twice, once each, and the new forms never warn.
	if _, stderr, _ := mainLine(t, "new", "nowhere", "-d", "/a", "--setup"); strings.Count(stderr, "Warning:") != 2 {
		t.Errorf("two old forms:\n%s", stderr)
	}
	for _, line := range [][]string{{"--version"}, {"new", "nowhere", "-C", "/a", "--setup-script"}, {"host", "create", "rec", "--memory", "8G"}, {"host", "alerts", "log", "nowhere", "--lines", "5"}} {
		if _, stderr, _ := mainLine(t, line...); strings.Contains(stderr, "Warning") {
			t.Errorf("%s warns:\n%s", line, stderr)
		}
	}
}

// With --json, the warning of an old flag or value is one JSON line on
// stderr, before the error (cli-spec 3.1, rule 5, and 7.4).
func TestOldFormWarnsAsJSON(t *testing.T) {
	withRecorder(t)
	for _, c := range []struct {
		line     []string
		old, now string
	}{
		{[]string{"host", "alerts", "log", "nowhere", "-n", "5", "--json"}, "-n", "--lines"},
		{[]string{"new", "nowhere", "-d", "/srv", "--json"}, "-d", "-C"},
	} {
		_, stderr, _ := mainLine(t, c.line...)
		lines := strings.Split(strings.TrimSpace(stderr), "\n")
		var w struct {
			V       int
			Warning map[string]string
		}
		if len(lines) != 2 || json.Unmarshal([]byte(lines[0]), &w) != nil || w.V != 1 || w.Warning["code"] != "old_name" ||
			w.Warning["old"] != c.old || w.Warning["new"] != c.now || w.Warning["until_version"] != "0.4.0" || !strings.HasPrefix(lines[1], `{"v":1,"error":`) {
			t.Errorf("%s: stderr\n%s", c.line, stderr)
		}
	}
}

// The warning has the form of cli-spec 3.1, in yellow with colour.
func TestTheWarningLine(t *testing.T) {
	o, _ := findOld("new -d")
	want := `Warning: "-d" is now "-C". "-d" works until v0.4.0 and 11 November 2027, whichever comes later.`
	if got := oldWarning(o, false); got != want {
		t.Errorf("the warning is\n%s\nwant\n%s", got, want)
	}
	if got := oldWarning(o, true); got != "\x1b[33m"+want+"\x1b[0m" {
		t.Errorf("the warning in colour is %q", got)
	}
}

// Each row ends two releases after its version, and 12 months after the
// day of that release (cli-spec 3.1).
func TestTheEndOfEachOldForm(t *testing.T) {
	semver := regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)$`)
	seen := map[string]bool{}
	for _, o := range oldForms {
		if seen[o.ID] {
			t.Errorf("two rows are %q", o.ID)
		}
		seen[o.ID] = true
		m := semver.FindStringSubmatch(o.Version)
		day, err := time.Parse(time.DateOnly, releaseDays[o.Version])
		if m == nil || err != nil || o.Old == "" || o.New == "" || o.Kind == "" {
			t.Errorf("%q: the row lacks a version, the day of its release or a form: %+v", o.ID, o)
			continue
		}
		minor, _ := strconv.Atoi(m[2])
		v, d := o.end()
		if want := m[1] + "." + strconv.Itoa(minor+2) + ".0"; v != want {
			t.Errorf("%q ends at %s, want %s", o.ID, v, want)
		}
		if !d.Equal(day.AddDate(1, 0, 0)) {
			t.Errorf("%q ends on %s, want 12 months after %s", o.ID, d, day)
		}
	}
	if v := twoReleasesAfter("1.9.3"); v != "1.11.0" {
		t.Errorf("1.9.3 ends at %s", v)
	}
}

// flagKinds are the kinds of old form that a flag or a flag's value keeps.
// A row of another kind, such as C4's "JSON key" or C5's "command", has no
// flag.
var flagKinds = []string{"flag", "short flag", "value"}

// rowsWithoutTheirFlag lists each problem with the flags that keep the
// rows of flagKinds: a row whose old form no flag keeps, or keeps on
// another command, and a hidden old flag that help shows.
func rowsWithoutTheirFlag(root *cobra.Command) []string {
	var problems []string
	used := map[string]string{}
	eachFlag(root, func(c *cobra.Command, f *pflag.Flag) {
		ids := f.Annotations[oldKey]
		if v, ok := f.Value.(*sizeValue); ok && v.old != "" {
			ids = append(ids, v.old)
		}
		for _, id := range ids {
			used[id] = strings.TrimSpace(shortPath(c))
			if !f.Hidden && len(f.Annotations[oldKey]) > 0 {
				problems = append(problems, c.CommandPath()+" --"+f.Name+" keeps an old form, and help shows it")
			}
		}
	})
	for _, o := range oldForms {
		if !slices.Contains(flagKinds, o.Kind) {
			continue
		}
		path, ok := used[o.ID]
		if !ok {
			problems = append(problems, "no flag keeps the old form "+strconv.Quote(o.ID))
		} else if path != o.Cmd {
			problems = append(problems, "the old form "+strconv.Quote(o.ID)+" is on "+strconv.Quote(path)+", and its row says "+strconv.Quote(o.Cmd))
		}
	}
	return problems
}

// Every row of a flag kind has the old form that it names in the tree, so
// a release that removes the row removes the old form.
func TestEveryRowHasItsOldForm(t *testing.T) {
	for _, p := range rowsWithoutTheirFlag(Root()) {
		t.Error(p)
	}
}

// Rows of other kinds, as C4's row of the JSON keys of host ls and C5's
// command rows, need no flag. They still have an end, a reference entry
// and a warning, when the tables of C4, C5 and C6 come together.
func TestRowsOfOtherKinds(t *testing.T) {
	old := oldForms
	if o, ok := findOld("host ls --json"); !ok || o.Kind != "JSON key" {
		t.Fatalf("C4's row of the JSON keys of host ls is missing")
	}
	oldForms = append(slices.Clone(oldForms),
		oldForm{ID: "kill", Cmd: "kill", Old: "kill", New: "rm", Kind: "command", Version: "0.2.0", Code: "old_name"})
	t.Cleanup(func() { oldForms = old })
	for _, p := range rowsWithoutTheirFlag(Root()) {
		t.Error(p)
	}
	if got := OldNames("host ls"); len(got) != 1 || got[0].Until != "v0.4.0 and 11 November 2027, whichever comes later" {
		t.Errorf("OldNames(host ls) = %+v", got)
	}
	cmd := Root()
	var b strings.Builder
	cmd.SetErr(&b)
	o, _ := findOld("kill")
	warnOld(cmd, o)
	warnOld(cmd, o)
	if want := `Warning: "kill" is now "rm". "kill" works until v0.4.0 and 11 November 2027, whichever comes later.` + "\n"; b.String() != want {
		t.Errorf("the warning of kill is %q, want %q", b.String(), want)
	}
}

// The shell's completion of a word never warns (cli-spec 3.3). Cobra runs
// only its __complete command, which reads no flags of its own, and never
// the hooks of the command that it completes. It still parses that
// command's flags, so an old form that warned when its flag was set would
// warn here. This test checks the whole rule: deprecated.go has no guard of
// its own for completion, because none is reached.
func TestCompletionNeverWarns(t *testing.T) {
	withRecorder(t)
	for _, line := range [][]string{
		{cobra.ShellCompRequestCmd, "new", "box", "-d", "/srv", ""},
		{cobra.ShellCompRequestCmd, "new", "box", "--setup", "--no-setup", "-"},
		{cobra.ShellCompRequestCmd, "-v", ""},
		{cobra.ShellCompNoDescRequestCmd, "agents", "install", "claude", "--force", ""},
		{cobra.ShellCompRequestCmd, "host", "alerts", "set", "box", "--token-cmd", "x", ""},
		{cobra.ShellCompRequestCmd, "host", "alerts", "log", "box", "-n", "5", ""},
		{cobra.ShellCompRequestCmd, "host", "create", "rec", "--memory", "8", ""},
	} {
		stderr, _ := captureStderr(t, func() int {
			_, errOut, code := mainLine(t, line...)
			os.Stderr.WriteString(errOut)
			return code
		})
		// Cobra always tells a person who runs __complete by hand how it
		// ended, and the shell's script drops the line. Nothing else may
		// come.
		if rest := regexp.MustCompile(`(?m)^Completion ended with directive: \w+\n`).ReplaceAllString(stderr, ""); rest != "" {
			t.Errorf("%s wrote to stderr:\n%s", line, stderr)
		}
	}
}

// withoutOld removes the rows ids from the table for the test, as a
// release does after their end. With no ids, it removes every row.
func withoutOld(t *testing.T, ids ...string) {
	t.Helper()
	old := oldForms
	var kept []oldForm
	for _, o := range oldForms {
		if len(ids) > 0 && !slices.Contains(ids, o.ID) {
			kept = append(kept, o)
		}
	}
	oldForms = kept
	t.Cleanup(func() { oldForms = old })
}

// After its end, -d is the short form of --detach. A script that still
// passes a folder after -d then fails loudly (cli-spec 10.4).
func TestAfterTheEndOfDashD(t *testing.T) {
	hosttest.New(t)
	if err := client.SaveHostsContext(context.Background(), []client.Host{{Name: "box", SSH: "box"}}); err != nil {
		t.Fatal(err)
	}
	withoutOld(t, "new -d")
	for _, path := range [][]string{{"new"}, {"resume"}} {
		c, _, err := Root().Find(path)
		if err != nil {
			t.Fatal(err)
		}
		if f := c.Flags().ShorthandLookup("d"); f == nil || f.Name != "detach" {
			t.Errorf("%s -d is %v, want --detach", path, f)
		}
	}
	for _, p := range shortFlagProblems(Root()) {
		t.Error(p)
	}
	if got, code := lineErr(t, "new", "box", "-d", "~/src"); code != 2 || !strings.HasPrefix(got, "Error: new takes one host. A command goes after --.\n") ||
		!strings.Contains(got, "Try: "+brand.Name+" new box -- htop (a folder goes after -C)") {
		t.Errorf("new box -d ~/src after the end = %d:\n%s", code, got)
	}
	if got, code := lineErr(t, "new", "-d", "~/src"); code != 3 || !strings.Contains(got, `no host named "~/src"`) {
		t.Errorf("new -d ~/src after the end = %d:\n%s", code, got)
	}
	if !liveOld("new --setup") {
		t.Error("the other rows went too")
	}
}

// After their end, the old forms fail as any unknown flag or bad value
// does, with exit code 2.
func TestAfterTheEndTheOldFormsFail(t *testing.T) {
	withRecorder(t)
	withoutOld(t)
	for _, c := range []struct {
		line []string
		want string
	}{
		{[]string{"-v"}, brand.Name + " has no flag -v"},
		{[]string{"new", "box", "--setup"}, "new has no flag --setup"},
		{[]string{"new", "box", "--no-setup"}, "new has no flag --no-setup"},
		{[]string{"agents", "install", "claude", "--force"}, "agents install has no flag --force"},
		{[]string{"host", "alerts", "set", "box", "--token-cmd", "x"}, "host alerts set has no flag --token-cmd"},
		{[]string{"host", "alerts", "log", "box", "-n", "5"}, "host alerts log has no flag -n"},
		{[]string{"host", "create", "rec", "--memory", "8"}, `--memory "8" is not a size, such as 4G`},
		{[]string{"host", "create", "rec", "--disk", "40"}, `--disk "40" is not a size, such as 4G`},
	} {
		_, stderr, code := mainLine(t, c.line...)
		if code != 2 || !strings.Contains(stderr, "Error: "+c.want) || strings.Contains(stderr, "Warning") {
			t.Errorf("%s after the end = %d:\n%s", strings.Join(c.line, " "), code, stderr)
		}
	}
}

// new -d <folder> starts the session in that folder, as it did, and warns.
func TestNewWithTheOldDashD(t *testing.T) {
	th := newTestHost(t)
	dir := filepath.Join(th.h.Home, "proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	out, stderr, code := mainLine(t, "new", "box", "-d", dir, "--detach", "--name", "old-d", "--", "sleep", "60")
	m := startedRE.FindStringSubmatch(stderr)
	if code != 0 || m == nil || !strings.HasPrefix(stderr, warningOf(t, "new -d", "", "")) || strings.Count(stderr, "Warning:") != 1 {
		t.Fatalf("new -d = %d, %q:\n%s", code, out, stderr)
	}
	if _, stderr, code := mainLine(t, "new", "box", "--setup", "--no-setup", "--detach"); code != 2 || !strings.Contains(stderr, "Error: --setup-script and --no-setup-script cannot go together.") {
		t.Errorf("new box --setup --no-setup = %d:\n%s", code, stderr)
	}
	var list []hostSessions
	if err := json.Unmarshal([]byte(ok(t, "ls", "--json")), &list); err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	for _, r := range list {
		for _, s := range r.Sessions {
			if s.ID == m[1] {
				found = append(found, s.Cwd)
			}
		}
	}
	if len(found) != 1 {
		t.Fatalf("ls shows session %s %d times: %+v", m[1], len(found), list)
	}
	got, err := filepath.EvalSymlinks(found[0])
	if err != nil || got != want {
		t.Errorf("the session runs in %q (%v), want %q", found[0], err, want)
	}
}

// An error about a hidden flag that keeps an old short form names it as
// the user typed it, as -d, never as --d.
func TestErrorsNameTheOldShortForm(t *testing.T) {
	hosttest.New(t)
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"new", "box", "-d"}, "Error: -d needs a value.\n"},
		{[]string{"host", "alerts", "log", "box", "-n", "abc"}, "Error: -n \"abc\" is not a whole number.\n"},
		{[]string{"new", "box", "-C"}, "Error: --dir needs a value.\n"},
	} {
		got, code := lineErr(t, c.args...)
		if code != 2 || !strings.HasPrefix(got, c.want) {
			t.Errorf("%s = %d:\n%s", c.args, code, got)
		}
	}
}

// After its end, an old flag gets the unknown-flag error, and "Did you
// mean" names the new flag (cli-spec 3.1, rule 8).
func TestAnEndedOldFlagNamesTheNewOne(t *testing.T) {
	hosttest.New(t)
	withoutOld(t)
	old := flagSynonyms
	flagSynonyms = map[string]map[string]string{
		"":                {"-v": "--version"},
		"host alerts set": {"--token-cmd": "--token-command"},
		"host alerts log": {"-n": "--lines"},
		"agents install":  {"--force": "--reinstall"},
	}
	t.Cleanup(func() { flagSynonyms = old })
	for _, c := range []struct {
		args      []string
		mean, try string
	}{
		{[]string{"host", "alerts", "set", "box", "--token-cmd", "x"}, `"--token-command"`, "host alerts set box --token-command x"},
		{[]string{"host", "alerts", "log", "box", "-n", "5"}, `"--lines"`, "host alerts log box --lines 5"},
		{[]string{"agents", "install", "claude", "--force"}, `"--reinstall"`, "agents install claude --reinstall"},
		{[]string{"-v"}, `"--version"`, "--version"},
	} {
		got, code := lineErr(t, c.args...)
		if code != 2 || !strings.Contains(got, "Did you mean "+c.mean+"?") || !strings.Contains(got, "Try: "+brand.Name+" "+c.try+"\n") {
			t.Errorf("%s = %d:\n%s", c.args, code, got)
		}
	}
}

// Every new flag that flagSynonyms names exists on its command.
func TestFlagSynonymsNameRealFlags(t *testing.T) {
	for path, olds := range flagSynonyms {
		c, _, err := Root().Find(strings.Fields(path))
		if err != nil || shortPath(c) != path {
			t.Errorf("no command %q", path)
			continue
		}
		for old, now := range olds {
			if c.Flags().Lookup(strings.TrimPrefix(now, "--")) == nil {
				t.Errorf("%s: %s names %s, which does not exist", path, old, now)
			}
		}
	}
}
