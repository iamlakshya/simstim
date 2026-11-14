package cli

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/diag"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/provider"
)

// shortFlags is the table of cli-spec 10.1: each short flag and the one
// long flag that it is.
var shortFlags = map[string]string{
	"a": "agent", "C": "dir", "d": "detach", "f": "force", "h": "help", "n": "name", "w": "worktree", "y": "yes",
}

// eachFlag calls fn with every flag of every command under root, with
// the help flag that Cobra adds when a command runs, and the root's
// version flag.
func eachFlag(root *cobra.Command, fn func(c *cobra.Command, f *pflag.Flag)) {
	root.InitDefaultVersionFlag()
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		c.InitDefaultHelpFlag()
		c.Flags().VisitAll(func(f *pflag.Flag) { fn(c, f) })
		c.PersistentFlags().VisitAll(func(f *pflag.Flag) {
			if c.Flags().Lookup(f.Name) != f {
				fn(c, f)
			}
		})
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(root)
}

// isOld reports whether f is a hidden flag that keeps an old form.
func isOld(f *pflag.Flag) bool { return f.Hidden && len(f.Annotations[oldKey]) > 0 }

// shortFlagProblems lists each short flag under root that the table of
// cli-spec 10.1 does not give, and each letter with two meanings. It skips
// a hidden flag that keeps an old form, such as -d for --dir, until its
// end.
func shortFlagProblems(root *cobra.Command) []string {
	var problems []string
	meanings := map[string]map[string]bool{}
	eachFlag(root, func(c *cobra.Command, f *pflag.Flag) {
		if f.Shorthand == "" || isOld(f) {
			return
		}
		if meanings[f.Shorthand] == nil {
			meanings[f.Shorthand] = map[string]bool{}
		}
		meanings[f.Shorthand][f.Name] = true
		if long, ok := shortFlags[f.Shorthand]; !ok || long != f.Name {
			problems = append(problems, fmt.Sprintf("%s: -%s is --%s, which the table of short flags (cli-spec 10.1) does not have", c.CommandPath(), f.Shorthand, f.Name))
		}
	})
	for short, longs := range meanings {
		if len(longs) > 1 {
			problems = append(problems, fmt.Sprintf("-%s has %d meanings: %v", short, len(longs), longs))
		}
	}
	return problems
}

// Each short flag has one meaning everywhere (cli-spec 10.1).
func TestShortFlagsHaveOneMeaning(t *testing.T) {
	for _, p := range shortFlagProblems(Root()) {
		t.Error(p)
	}
}

// The test fails when two commands give one letter two meanings, as the
// acceptance of task C6 asks.
func TestTheShortFlagTestFindsTwoMeanings(t *testing.T) {
	root := Root()
	c, _, err := root.Find([]string{"host", "ls"})
	if err != nil {
		t.Fatal(err)
	}
	c.Flags().BoolP("names", "n", false, "")
	got := strings.Join(shortFlagProblems(root), "\n")
	if !strings.Contains(got, "host ls: -n is --names") || !strings.Contains(got, "-n has 2 meanings") {
		t.Errorf("-n as --names and as --name went unseen:\n%s", got)
	}
	root = Root()
	c, _, _ = root.Find([]string{"version"})
	c.Flags().BoolP("verbose", "v", false, "")
	if got := strings.Join(shortFlagProblems(root), "\n"); !strings.Contains(got, "version: -v is --verbose") {
		t.Errorf("-v went unseen:\n%s", got)
	}
}

// sharedFlags are the flags of cli-spec 10.2: each comes from its helper
// in flags.go, wherever a command takes it.
var sharedFlags = []string{
	"yes", "force", "confirm", "json", "dry-run", "no-input", "no-color", "debug", "debug-file", "name", "host", "detach",
	"memory-max", "memory", "cpus", "secret", "for", "all", "follow", "fix", "label", "token-command", "root-step", "images",
	"profile", "in", "count",
}

func TestSharedFlagsComeFromTheirHelpers(t *testing.T) {
	n := 0
	eachFlag(Root(), func(c *cobra.Command, f *pflag.Flag) {
		if !slices.Contains(sharedFlags, f.Name) || isOld(f) {
			return
		}
		n++
		if got := f.Annotations[sharedKey]; len(got) != 1 || got[0] != f.Name {
			t.Errorf("%s defines --%s without its helper in flags.go (cli-spec 10.2)", c.CommandPath(), f.Name)
		}
	})
	if n < 25 {
		t.Errorf("found only %d shared flags: the test no longer reads the tree", n)
	}
}

// parsed is the flag name of the command that line names, after the flags
// of the line are parsed.
func parsed(t *testing.T, name string, line ...string) *pflag.Flag {
	t.Helper()
	c, rest, err := Root().Find(line)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ParseFlags(rest); err != nil {
		t.Fatalf("%s: %v", strings.Join(line, " "), err)
	}
	f := c.Flags().Lookup(name)
	if f == nil {
		t.Fatalf("%s has no --%s", c.CommandPath(), name)
	}
	return f
}

// Every list of names takes a repeated flag and commas, and the two are the
// same (cli-spec 10.3).
func TestListsTakeARepeatedFlagAndCommas(t *testing.T) {
	hosttest.New(t)
	for _, c := range []struct {
		line []string
		flag string
	}{
		{[]string{"new", "box"}, "secret"},
		{[]string{"resume", "box/s"}, "secret"},
		{[]string{"setup"}, "agents"},
		{[]string{"host", "add", "box"}, "label"},
		{[]string{"host", "alerts", "set", "box"}, "events"},
		{[]string{"debug", "start", "box/s"}, "only"},
	} {
		repeated := parsed(t, c.flag, append(slices.Clone(c.line), "--"+c.flag, "a", "--"+c.flag, "b , c", "--"+c.flag+"=d")...)
		commas := parsed(t, c.flag, append(slices.Clone(c.line), "--"+c.flag, "a,b,c,,d")...)
		for _, f := range []*pflag.Flag{repeated, commas} {
			l, ok := f.Value.(*listValue)
			if !ok {
				t.Errorf("%s --%s is a %T, not a list", strings.Join(c.line, " "), c.flag, f.Value)
				continue
			}
			if got := l.GetSlice(); !slices.Equal(got, []string{"a", "b", "c", "d"}) {
				t.Errorf("%s --%s = %q", strings.Join(c.line, " "), c.flag, got)
			}
		}
	}
	if got := parsed(t, "debug", "ls", "--debug=net", "--debug=attach,memory").Value.String(); got != "net,attach,memory" {
		t.Errorf("--debug twice = %q", got)
	}
	if got := parsed(t, "debug", "ls", "--debug").Value.String(); got != diag.CatList {
		t.Errorf("--debug alone = %q, want %q", got, diag.CatList)
	}
	if _, stderr, code := mainLine(t, "debug", "start", "box/s", "--only", ","); code != 2 || !strings.Contains(stderr, "Error: --only: name at least one category") {
		t.Errorf("debug start --only , = %d:\n%s", code, stderr)
	}
	// An empty --only is every category, as before: the run goes on to
	// the host, which does not exist here.
	if _, stderr, code := mainLine(t, "debug", "start", "box/s", "--only", ""); code != 3 || !strings.Contains(stderr, `no host named "box"`) {
		t.Errorf("debug start --only '' = %d:\n%s", code, stderr)
	}
}

// A list flag gives its names through pflag.SliceValue. Its type is its
// own, so that pflag's GetStringSlice refuses it, and never reads its
// text wrong.
func TestAListGivesItsNames(t *testing.T) {
	for _, line := range [][]string{{"setup"}, {"setup", "--agents", "claude,codex", "--agents", "aider"}} {
		f := parsed(t, "agents", line...)
		sv, ok := f.Value.(pflag.SliceValue)
		if !ok {
			t.Fatalf("--agents is a %T", f.Value)
		}
		want := []string(nil)
		if len(line) > 1 {
			want = []string{"claude", "codex", "aider"}
		}
		if got := sv.GetSlice(); !slices.Equal(got, want) {
			t.Errorf("%s: GetSlice = %q, want %q", line, got, want)
		}
		c, _, _ := Root().Find(line[:1])
		_ = c.ParseFlags(line[1:])
		if got, err := c.Flags().GetStringSlice("agents"); err == nil {
			t.Errorf("%s: GetStringSlice = %q, and no error", line, got)
		}
	}
	if f := parsed(t, "agents", "setup"); f.Value.Type() == "stringSlice" || f.DefValue != "" {
		t.Errorf("--agents is of type %q, with the default %q", f.Value.Type(), f.DefValue)
	}
}

func TestSplitList(t *testing.T) {
	for _, c := range [][2]string{{"a", "a"}, {"a,b", "a|b"}, {" a , b ", "a|b"}, {"a,,b,", "a|b"}, {"", ""}, {",", ""}} {
		if got := strings.Join(splitList(c[0]), "|"); got != c[1] {
			t.Errorf("splitList(%q) = %q, want %q", c[0], got, c[1])
		}
	}
	for _, c := range []struct {
		value     string
		item      int
		count, at int
	}{{"a,b", 1, 2, 1}, {"a,,b", 1, 2, 2}, {" a ", 0, 1, 0}, {"a", 1, 1, -1}, {"", 0, 0, -1}} {
		if count, at := listItem(c.value, c.item); count != c.count || at != c.at {
			t.Errorf("listItem(%q, %d) = %d, %d, want %d, %d", c.value, c.item, count, at, c.count, c.at)
		}
	}
}

func TestParseSize(t *testing.T) {
	for in, want := range map[string]int64{
		"512M": 512 << 20, "4G": 4 << 30, "4g": 4 << 30, "4Gi": 4 << 30, "4GiB": 4 << 30, "4GB": 4 << 30, "1.5G": 1536 << 20, "1T": 1 << 40, "100M": 100 << 20, "8 G": 8 << 30,
		"1K": 1 << 10, "0.0009765625K": 1,
	} {
		if got, bare, err := parseSize(in, false); err != nil || got != want || bare {
			t.Errorf("parseSize(%q) = %d, %v, %v, want %d", in, got, bare, err, want)
		}
	}
	for in, why := range map[string]string{
		"8": "has no unit", "4X": "is not a size", "": "is not a size", "0G": "is not a size", "-1G": "is not a size", "99999999T": "is too big",
		"0.0009K": "is not a size", "0.0000000001G": "is not a size", "0": "has no unit",
	} {
		if got, _, err := parseSize(in, false); err == nil || !strings.Contains(err.Error(), why) {
			t.Errorf("parseSize(%q) = %d, %v, want an error that it %s", in, got, err, why)
		}
	}
	// The old form: a bare number is GiB, through the same rules, and 0 or
	// less is the default, as the old whole-number flag took it.
	for _, c := range []struct {
		in   string
		want int64
		bare bool
	}{
		{"8", 8 << 30, true}, {"1.5", 1536 << 20, true}, {"8G", 8 << 30, false}, {" 8 ", 8 << 30, true},
		{"0.000000001", 1, true}, {"0", 0, true}, {"-1", 0, true}, {"0.0", 0, true},
		// Whole numbers in the forms that pflag's int flag took.
		{"010", 8 << 30, true}, {"0x10", 16 << 30, true}, {"+8", 8 << 30, true}, {"0b11", 3 << 30, true},
		{"0o7", 7 << 30, true}, {"1_0", 10 << 30, true}, {"-0x1", 0, true},
	} {
		got, bare, err := parseSize(c.in, true)
		if err != nil || got != c.want || bare != c.bare {
			t.Errorf("parseSize(%q, GiB) = %d, %v, %v, want %d, %v", c.in, got, bare, err, c.want, c.bare)
		}
	}
	for _, in := range []string{"0.0000000001", "1e3", "-1.5", "99999999999"} {
		if n, _, err := parseSize(in, true); err == nil {
			t.Errorf("parseSize(%q, GiB) took it as %d", in, n)
		}
	}
}

// An idle time follows the one rule that host idle, host create
// --idle-stop and host set idle= share: trimmed, "never" in any case kept
// as "never", else a Go duration of a minute or more.
func TestParseIdle(t *testing.T) {
	for in, want := range map[string]string{
		"30m": "30m", " 2h ": "2h", "1m": "1m", "60s": "60s", "never": "never", "NEVER": "never", " Never ": "never",
	} {
		if got, err := parseIdle(in); err != nil || got != want {
			t.Errorf("parseIdle(%q) = %q, %v, want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"30s", "59s", "0", "-1m", "soon", "", "never ever"} {
		if got, err := parseIdle(in); err == nil {
			t.Errorf("parseIdle(%q) = %q, and no error", in, got)
		}
	}
}

func TestParseDuration(t *testing.T) {
	for _, c := range []struct {
		in    string
		never bool
		want  time.Duration
		off   bool
		err   string
	}{
		{"30s", false, 30 * time.Second, false, ""},
		{"10m", true, 10 * time.Minute, false, ""},
		{" 2h ", false, 2 * time.Hour, false, ""},
		{"never", true, 0, true, ""},
		{"Never", true, 0, true, ""},
		{"never", false, 0, false, `"never" is not a time, such as 30s, 10m or 2h`},
		{"soon", true, 0, false, `"soon" is not a time, such as 30s, 10m or 2h, or never`},
		{"30", false, 0, false, "is not a time"},
		{"-1m", false, 0, false, "is not a time"},
	} {
		d, off, err := parseDuration(c.in, c.never)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("parseDuration(%q, %v) = %v, %v, want an error %q", c.in, c.never, d, err, c.err)
			}
			continue
		}
		if err != nil || d != c.want || off != c.off {
			t.Errorf("parseDuration(%q, %v) = %v, %v, %v", c.in, c.never, d, off, err)
		}
	}
}

// recorder is a provider that keeps the request that it gets, and makes
// nothing.
type recorder struct{ got *provider.Request }

func (recorder) Name() string { return "rec" }

func (r recorder) Create(_ context.Context, req provider.Request) (client.Host, error) {
	*r.got = req
	return client.Host{}, errors.New("the recorder makes nothing")
}

func (recorder) Open(client.Host) (client.Lifecycle, error) { return nil, errors.New("no machine") }

// withRecorder adds the provider "rec", and returns the request that it
// gets.
func withRecorder(t *testing.T) *provider.Request {
	t.Helper()
	hosttest.New(t)
	got := &provider.Request{}
	old := providers
	providers = append(slices.Clone(providers), recorder{got})
	t.Cleanup(func() { providers = old })
	return got
}

// Sizes take units, and a bad value is a usage error that names the flag
// (cli-spec 4.4 and 10.3).
func TestSizesAndTimesOfHostCreate(t *testing.T) {
	got := withRecorder(t)
	if _, _, code := mainLine(t, "host", "create", "rec", "--memory", "1.5G", "--disk", "512M", "--cpus", "3", "--idle-stop", "never"); code != 1 {
		t.Fatalf("host create rec exits with %d", code)
	}
	if got.Memory != 1536<<20 || got.Disk != 512<<20 || got.CPUs != 3 || got.IdleStop != "never" {
		t.Errorf("the provider got %+v", *got)
	}
	if _, _, code := mainLine(t, "host", "create", "rec"); code != 1 || got.Memory != 4<<30 || got.Disk != 40<<30 || got.IdleStop != "" {
		t.Errorf("the defaults: exit %d, %+v", code, *got)
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--memory", "4X"}, `--memory "4X" is not a size, such as 4G.`},
		{[]string{"--disk", "0G"}, `--disk "0G" is not a size, such as 4G.`},
		{[]string{"--idle-stop", "soon"}, `--idle-stop "soon" is not a time of a minute or more, such as 30m, or never.`},
		{[]string{"--idle-stop", "30s"}, `--idle-stop "30s" is not a time of a minute or more, such as 30m, or never.`},
	} {
		_, stderr, code := mainLine(t, append([]string{"host", "create", "rec"}, c.args...)...)
		if code != 2 || !strings.Contains(stderr, "Error: "+c.want) || !strings.Contains(stderr, "Try: "+brand.Name+" host create --help") {
			t.Errorf("%s: exit %d\n%s", c.args, code, stderr)
		}
	}
}

// The old bare numbers keep their old meaning: 0 or less is the default,
// and warns. An empty --idle-stop is the default, as before. A size that
// comes to less than one byte is refused, with or without a unit.
func TestOldMeaningsAndBoundsOfHostCreate(t *testing.T) {
	got := withRecorder(t)
	for _, c := range []struct {
		args         []string
		memory, disk int64
		warns        []string
	}{
		{[]string{"--memory", "0"}, 0, 40 << 30, []string{`"--memory 0" is now "--memory 4G"`}},
		{[]string{"--memory", "-1", "--disk", "0"}, 0, 0, []string{`"--memory -1" is now "--memory 4G"`, `"--disk 0" is now "--disk 40G"`}},
		{[]string{"--memory", "0.000000001", "--disk", "1K"}, 1, 1 << 10, []string{`"--memory 0.000000001" is now "--memory 0.000000001G"`}},
		{[]string{"--memory", "0.0009765625K", "--disk", "0.000000001G"}, 1, 1, nil},
		// The old forms that pflag's int flag took mean what they meant.
		{[]string{"--memory", "010", "--disk", "0x10"}, 8 << 30, 16 << 30, []string{`"--memory 010" is now "--memory 8G"`, `"--disk 0x10" is now "--disk 16G"`}},
		{[]string{"--memory", "+2", "--disk", "0b11"}, 2 << 30, 3 << 30, []string{`"--memory +2" is now "--memory 2G"`, `"--disk 0b11" is now "--disk 3G"`}},
		{[]string{"--disk", "-0x1"}, 4 << 30, 0, []string{`"--disk -0x1" is now "--disk 40G"`}},
		// A later new form replaces the value, and the old form still warns.
		{[]string{"--memory", "8", "--memory", "2G"}, 2 << 30, 40 << 30, []string{`"--memory 8" is now "--memory 8G"`}},
		{[]string{"--memory", "2G", "--memory", "8"}, 8 << 30, 40 << 30, []string{`"--memory 8" is now "--memory 8G"`}},
		{[]string{"--disk", "8", "--disk", "16"}, 4 << 30, 16 << 30, []string{`"--disk 8" is now "--disk 8G"`}},
	} {
		*got = provider.Request{}
		_, stderr, code := mainLine(t, append([]string{"host", "create", "rec"}, c.args...)...)
		if code != 1 || got.Memory != c.memory || got.Disk != c.disk {
			t.Errorf("%s: exit %d, memory %d, disk %d, want %d and %d:\n%s", c.args, code, got.Memory, got.Disk, c.memory, c.disk, stderr)
		}
		if n := strings.Count(stderr, "Warning:"); n != len(c.warns) {
			t.Errorf("%s: %d warnings, want %d:\n%s", c.args, n, len(c.warns), stderr)
		}
		for _, w := range c.warns {
			if !strings.Contains(stderr, "Warning: "+w+". ") {
				t.Errorf("%s: no warning %s:\n%s", c.args, w, stderr)
			}
		}
	}
	for _, args := range [][]string{
		{"--memory", "0.0000000001"}, {"--disk", "0.0000000001"}, {"--memory", "0.0009K"}, {"--disk", "0.0009K"}, {"--memory", "0G"},
	} {
		_, stderr, code := mainLine(t, append([]string{"host", "create", "rec"}, args...)...)
		if code != 2 || !strings.Contains(stderr, "is not a size, such as 4G.") {
			t.Errorf("%s: exit %d:\n%s", args, code, stderr)
		}
	}
	got.IdleStop = "unset"
	if _, stderr, code := mainLine(t, "host", "create", "rec", "--idle-stop", ""); code != 1 || got.IdleStop != "" {
		t.Errorf("--idle-stop '' = %d, %q:\n%s", code, got.IdleStop, stderr)
	}
}

// host idle keeps the time without the spaces that it read past.
func TestHostIdleSavesTheTrimmedTime(t *testing.T) {
	hosttest.New(t)
	if err := client.SaveHostsContext(context.Background(), []client.Host{{Name: "box", SSH: "box", Machine: &client.Machine{Provider: "docker", ID: "c1"}}}); err != nil {
		t.Fatal(err)
	}
	for in, want := range map[string]string{" 30m ": "30m", "2h": "2h", " Never ": "never"} {
		if _, stderr, code := mainLine(t, "host", "idle", "box", in); code != 0 {
			t.Fatalf("host idle box %q = %d:\n%s", in, code, stderr)
		}
		h, err := client.FindHost("box")
		if err != nil || h.Machine.IdleStop != want {
			t.Errorf("host idle box %q saved %q, %v, want %q", in, h.Machine.IdleStop, err, want)
		}
	}
	// A sandbox says the time that it saved. It does not connect.
	if err := client.SaveHostsContext(context.Background(), []client.Host{{Name: "cloud", Machine: &client.Machine{Provider: "e2b", ID: "s1"}}}); err != nil {
		t.Fatal(err)
	}
	if out, stderr, code := mainLine(t, "host", "idle", "cloud", " 30m "); code != 0 || out != "" || stderr != warningOf(t, "host idle", "", "")+"cloud: idle 15m → 30m.\ncloud pauses 30m after the last client goes, while no session works.\n" {
		t.Errorf("host idle cloud ' 30m ' = %d, %q:\n%s", code, out, stderr)
	}
}

// A name in a list that the flag took more than once is fixed in the use
// where the user typed it.
func TestTheTryLineFixesANameInARepeatedList(t *testing.T) {
	hosttest.New(t)
	for _, c := range []struct {
		args []string
		try  string
	}{
		{[]string{"setup", "--agents", "claude", "--agents", "cluade", "--yes"}, "setup --agents claude --agents claude --yes"},
		{[]string{"setup", "--agents", "codex,,cluade", "--yes"}, "setup --agents codex,,claude --yes"},
		{[]string{"setup", "--agents", "cluade", "--agents", "cluade", "--yes"}, "setup --agents claude --agents cluade --yes"},
	} {
		got, code := lineErr(t, c.args...)
		if code != 3 || !strings.Contains(got, "\nTry: "+brand.Name+" "+c.try+"\n") {
			t.Errorf("%s = %d:\n%s", c.args, code, got)
		}
	}
}

// new takes one host before "--" (isolation spec 14.14).
func TestNewTakesOneHost(t *testing.T) {
	hosttest.New(t)
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"new", "box", "htop"}, "Error: new takes one host. A command goes after --.\n\nTry: " + brand.Name + " new box -- htop (a folder goes after -C)\n"},
		{[]string{"new", "--", "htop"}, "Error: new needs <host>.\n"},
		{[]string{"new", "a b", "htop"}, "Error: new takes one host. A command goes after --.\n\nTry: " + brand.Name + " host ls\n"},
	} {
		got, code := lineErr(t, c.args...)
		if code != 2 || !strings.HasPrefix(got, c.want) {
			t.Errorf("%s = %d:\n%s", c.args, code, got)
		}
	}
}
