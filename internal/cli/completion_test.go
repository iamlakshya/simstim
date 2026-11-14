package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/diag"
)

func isolateCompletionEnv(t *testing.T) (cfgDir, stateDir string) {
	t.Helper()
	base := t.TempDir()
	t.Setenv("HOME", base)
	t.Setenv("USERPROFILE", base)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(base, "state"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(base, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(base, "cache"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(base, "run"))
	t.Setenv("GNUPGHOME", filepath.Join(base, "gnupg"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(base, "gitconfig"))
	t.Setenv("BASH_COMP_DEBUG_FILE", "")
	t.Setenv(diag.EnvFile, "")
	for _, d := range []string{"config", "state", "data", "cache", "run", "gnupg"} {
		if err := os.MkdirAll(filepath.Join(base, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	cfgDir = filepath.Join(base, "config", brand.Name)
	stateDir = filepath.Join(base, "state", brand.Name)
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	return cfgDir, stateDir
}

func execComplete(t *testing.T, root *cobra.Command, cmdName string, args ...string) (items []string, dir cobra.ShellCompDirective, stderr string) {
	t.Helper()
	if root == nil {
		root = Root()
	}
	return execCompleteContext(t, context.Background(), root, cmdName, args...)
}

func execCompleteContext(t *testing.T, ctx context.Context, root *cobra.Command, cmdName string, args ...string) (items []string, dir cobra.ShellCompDirective, stderr string) {
	t.Helper()
	full := append([]string{cmdName}, args...)
	var runErr error
	stdout, errOut := captureOutput(t, func() {
		root.SetArgs(full)
		runErr = root.ExecuteContext(withLine(ctx, full))
	})
	if runErr != nil {
		t.Fatalf("%s %v failed: %v\nstdout: %q\nstderr: %q", cmdName, args, runErr, stdout, errOut)
	}
	trimmed := strings.TrimSuffix(stdout, "\n")
	lines := strings.Split(trimmed, "\n")
	last := lines[len(lines)-1]
	if !strings.HasPrefix(last, ":") {
		t.Fatalf("%s %v missing directive line in %q", cmdName, args, stdout)
	}
	n, err := strconv.Atoi(last[1:])
	if err != nil {
		t.Fatalf("%s %v bad directive %q: %v", cmdName, args, last, err)
	}
	return lines[:len(lines)-1], cobra.ShellCompDirective(n), errOut
}

func writeCompletionFixtures(t *testing.T, cfgDir, stateDir string, now time.Time) {
	t.Helper()
	hostsTOML := `[[host]]
name = "atlas"
ssh = "dev@atlas.example"

[[host]]
name = "box"
ssh = "box"

[[host]]
name = "vm"
[host.machine]
provider = "lima"
id = "vm"
`
	if err := os.WriteFile(filepath.Join(cfgDir, "hosts.toml"), []byte(hostsTOML), 0o600); err != nil {
		t.Fatal(err)
	}
	configTOML := `[secrets]
ANTHROPIC_API_KEY = "echo never-run"
GITHUB_TOKEN = "echo never-run"

[profiles.review]

[profiles.untrusted]
`
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(configTOML), 0o600); err != nil {
		t.Fatal(err)
	}
	exitOne := 1
	cache := client.SessionCache{
		Sessions: map[string][]client.Session{
			"atlas": {
				{ID: "abcd2345", Name: "auth-fix", Agent: "claude", State: "working"},
				{ID: "efgh2345", Name: "dup", Agent: "codex", State: "idle"},
				{ID: "ijkl2345", Name: "dup", Agent: "claude", State: "needs_input"},
			},
			"box": {
				{ID: "mnop2345", Name: "web", Agent: "opencode", State: "errored", OOM: true, ExitCode: &exitOne},
				{ID: "qrst2345", Name: "", Agent: "shell", State: "exited"},
			},
			"gone": {
				{ID: "uvwx2345", Name: "orphan", Agent: "claude", State: "working"},
			},
		},
		Seen: map[string]time.Time{
			"atlas": now.Add(-30 * time.Second),
			"box":   now.Add(-5 * time.Minute),
			"gone":  now.Add(-10 * time.Second),
		},
	}
	b, err := json.Marshal(cache)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "launcher.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// syntheticFutureRoot adds a hidden-free test subcommand that exercises the
// isolation flags and positional completers scheduled for H4/H10/H14/H19/P1.12
// to test their existing fixed-value completers through Cobra's __complete.
func syntheticFutureRoot() *cobra.Command {
	root := Root()
	fullHostSettings := []hostSetting{
		{key: "idle", values: []string{"never"}},
		{key: "label.", prefix: true},
		{key: "tier", values: []string{"own", "throwaway", "shared"}},
		{key: "floor", values: []string{"none", "process", "gvisor", "microvm"}},
		{key: "drain", values: []string{"off"}},
	}
	fut := &cobra.Command{Use: "_spec14"}
	doorCmd := &cobra.Command{Use: "door <word>", Args: cobra.ExactArgs(1), ValidArgsFunction: firstArg(completeDoorWords), RunE: func(*cobra.Command, []string) error { return nil }}
	hsetCmd := &cobra.Command{
		Use:  "hset <host> <pair>...",
		Args: cobra.MinimumNArgs(1),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			if len(args) == 0 {
				return completeHosts(cmd, args, toComplete)
			}
			return completeHostSettings(fullHostSettings)(cmd, args, toComplete)
		},
		RunE: func(*cobra.Command, []string) error { return nil },
	}
	flagsCmd := &cobra.Command{Use: "flags", RunE: func(*cobra.Command, []string) error { return nil }}
	for _, f := range []string{"profile", "door", "wall", "tier", "preset", "root-step", "images"} {
		flagsCmd.Flags().String(f, "", f)
	}
	fut.AddCommand(doorCmd, hsetCmd, flagsCmd)
	root.AddCommand(fut)
	wireKnownFlagCompletions(root)
	return root
}

// Golden test of __complete for current values and reusable fixed-value completers.
func TestGoldenCompletion(t *testing.T) {
	cfgDir, stateDir := isolateCompletionEnv(t)
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	prevNow := compNow
	compNow = func() time.Time { return now }
	t.Cleanup(func() { compNow = prevNow })
	writeCompletionFixtures(t, cfgDir, stateDir, now)

	cases := []struct {
		title string
		fut   bool
		noDsc bool
		args  []string
	}{
		{title: "14.2 host argument", args: []string{"host", "show", ""}},
		{title: "14.2 --host prefix", args: []string{"ls", "--host", "a"}},
		{title: "14.2 address", args: []string{"attach", ""}},
		{title: "14.2 address without descriptions", noDsc: true, args: []string{"attach", "atlas/"}},
		{title: "14.2 --agent", args: []string{"new", "box", "--agent", ""}},
		{title: "14.2 agents install", args: []string{"agents", "install", ""}},
		{title: "14.2 setup --agents comma list", args: []string{"setup", "--agents", "claude,co"}},
		{title: "14.2 host set keys (current build)", args: []string{"host", "set", "vm", ""}},
		{title: "14.2 host set value (current build)", args: []string{"host", "set", "vm", "idle="}},
		{title: "14.2 host set keys (with isolation keys)", fut: true, args: []string{"_spec14", "hset", "vm", "idle=2h", ""}},
		{title: "14.2 host set tier values", fut: true, args: []string{"_spec14", "hset", "vm", "tier="}},
		{title: "14.2 host set floor values", fut: true, args: []string{"_spec14", "hset", "vm", "floor="}},
		{title: "14.2 host set drain values", fut: true, args: []string{"_spec14", "hset", "vm", "drain="}},
		{title: "14.2 --debug categories", args: []string{"ls", "--debug=attach,"}},
		{title: "14.2 debug profile kind", args: []string{"debug", "profile", "atlas/auth-fix", ""}},
		{title: "14.2 debug bundle host or session", args: []string{"debug", "bundle", ""}},
		{title: "14.2 completion shells", args: []string{"completion", ""}},
		{title: "14.2 door words (P1.12)", fut: true, args: []string{"_spec14", "door", ""}},
		{title: "14.2 --profile (P1.12)", fut: true, args: []string{"_spec14", "flags", "--profile", ""}},
		{title: "14.2 --wall", fut: true, args: []string{"_spec14", "flags", "--wall", ""}},
		{title: "14.2 --tier", fut: true, args: []string{"_spec14", "flags", "--tier", ""}},
		{title: "14.2 --preset", fut: true, args: []string{"_spec14", "flags", "--preset", ""}},
		{title: "14.2 --root-step", fut: true, args: []string{"_spec14", "flags", "--root-step", ""}},
		{title: "14.2 --images", fut: true, args: []string{"_spec14", "flags", "--images", ""}},
		{title: "14.2 --secret", args: []string{"new", "box", "--secret", ""}},
	}

	var b strings.Builder
	for _, tc := range cases {
		var r *cobra.Command
		if tc.fut {
			r = syntheticFutureRoot()
		}
		cmd := cobra.ShellCompRequestCmd
		if tc.noDsc {
			cmd = cobra.ShellCompNoDescRequestCmd
		}
		items, dir, errOut := execComplete(t, r, cmd, tc.args...)
		if errOut != "" {
			t.Errorf("%s wrote to stderr: %q", tc.title, errOut)
		}
		fmt.Fprintf(&b, "=== %s (%s) ===\n", tc.title, strings.Join(tc.args, " "))
		for _, it := range items {
			b.WriteString(it + "\n")
		}
		fmt.Fprintf(&b, ":%d\n\n", dir)
	}

	got := strings.ReplaceAll(b.String(), brand.Name, "<name>")
	golden := filepath.Join("testdata", "completion.golden")
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
		t.Errorf("completions differ from %s (run go test ./internal/cli -run TestGoldenCompletion -update):\n%s", golden, got)
	}
}

// Completion never dials SSH, invokes a provider or secret script, opens a
// machine, waits on a host lock, or writes the session cache (cli-spec 14.3).
func TestCompletionZeroNetworkAndLocks(t *testing.T) {
	cfgDir, stateDir := isolateCompletionEnv(t)
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	prevNow := compNow
	compNow = func() time.Time { return now }
	t.Cleanup(func() { compNow = prevNow })

	binDir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "touched")
	trapScript := filepath.Join(binDir, "trap.sh")
	body := "#!/bin/sh\necho ran >> " + marker + "\nexit 1\n"
	for _, p := range []string{filepath.Join(binDir, "ssh"), trapScript} {
		if err := os.WriteFile(p, []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	client.RegisterProvider("trap", func(client.Host) (client.Lifecycle, error) {
		t.Error("completion called OpenMachine on provider trap")
		return nil, fmt.Errorf("trap provider called")
	})

	hostsTOML := fmt.Sprintf(`[[host]]
name = "box"
ssh = "unreachable.invalid"
[host.provider]
script = %q
wake_on_attach = true

[[host]]
name = "traphost"
[host.machine]
provider = "trap"
id = "m-1"
`, trapScript)
	if err := os.WriteFile(filepath.Join(cfgDir, "hosts.toml"), []byte(hostsTOML), 0o600); err != nil {
		t.Fatal(err)
	}
	configTOML := fmt.Sprintf("[secrets]\nOPENAI_API_KEY = %q\n", trapScript)
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(configTOML), 0o600); err != nil {
		t.Fatal(err)
	}
	cache := client.SessionCache{
		Sessions: map[string][]client.Session{
			"box": {{ID: "abcd2345", Name: "work", Agent: "claude", State: "working"}},
		},
		Seen: map[string]time.Time{"box": now.Add(-10 * time.Second)},
	}
	cacheBytes, err := json.Marshal(cache)
	if err != nil {
		t.Fatal(err)
	}
	cacheFile := filepath.Join(stateDir, "launcher.json")
	if err := os.WriteFile(cacheFile, cacheBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	// Hold the host lock of "box" while completing: completion must not block.
	_, unlock, err := client.LockHost(context.Background(), "box", "test")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	holdCompletionHostsFileLock(t)
	defer client.ShortenHostsWait(200 * time.Millisecond)()

	queries := [][]string{
		{"ls", ""},
		{"new", ""},
		{"attach", ""},
		{"show", ""},
		{"rm", ""},
		{"resume", ""},
		{"host", "show", ""},
		{"host", "set", ""},
		{"host", "set", "box", ""},
		{"host", "alerts", ""},
		{"debug", "bundle", ""},
		{"debug", "profile", ""},
		{"agents", "install", ""},
		{"new", "box", "--secret", ""},
	}
	for _, q := range queries {
		root := Root()
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		items, dir, errOut := execCompleteContext(t, ctx, root, cobra.ShellCompRequestCmd, q...)
		cancel()
		if dir&cobra.ShellCompDirectiveError != 0 {
			t.Errorf("%v returned ShellCompDirectiveError (%d)", q, dir)
		}
		if errOut != "" {
			t.Errorf("%v wrote to stderr: %q", q, errOut)
		}
		want := "box"
		switch q[0] {
		case "attach", "show", "rm", "resume":
			want = "box/work"
		case "agents":
			want = "claude"
		case "debug":
			if q[1] == "profile" {
				want = "box/work"
			}
		case "new":
			if len(q) > 2 {
				want = "OPENAI_API_KEY"
			}
		case "host":
			if len(q) > 3 {
				want = "idle="
			}
		}
		found := false
		for _, item := range items {
			value, _, _ := strings.Cut(item, "\t")
			found = found || value == want
		}
		if !found {
			t.Errorf("%v dropped %q while files are locked: %q", q, want, items)
		}
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("completion executed ssh, a provider script or a secret command")
	}
	for name, want := range map[string]string{"hosts.toml": hostsTOML, "config.toml": configTOML} {
		b, err := os.ReadFile(filepath.Join(cfgDir, name))
		if err != nil || string(b) != want {
			t.Errorf("completion modified %s: %v", name, err)
		}
	}
	after, err := os.ReadFile(cacheFile)
	if err != nil || !bytes.Equal(after, cacheBytes) {
		t.Fatal("completion modified launcher.json")
	}
	if _, err := os.Stat(filepath.Join(stateDir, "last.json")); err == nil {
		t.Fatal("completion created last.json")
	}
}

// Cached session fields and host targets from untrusted sources are stripped
// of terminal escapes, tabs, newlines and bidirectional overrides.
func TestCompletionSanitizesUntrustedStrings(t *testing.T) {
	cfgDir, stateDir := isolateCompletionEnv(t)
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	prevNow := compNow
	compNow = func() time.Time { return now }
	t.Cleanup(func() { compNow = prevNow })

	hostsTOML := "[[host]]\nname = \"box\"\nssh = \"user@box\\u001b[31m\\tinjected\\nline\\u202e\"\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "hosts.toml"), []byte(hostsTOML), 0o600); err != nil {
		t.Fatal(err)
	}
	exitOne := 1
	cache := client.SessionCache{
		Sessions: map[string][]client.Session{
			"box": {
				{
					ID:       "abcd2345",
					Name:     "evil\x1b[31m-name\twith\nlines\x1b]52;c;ZWNobyBwd25lZA==\x07\x1bc\u009b32m",
					Agent:    "claude\x1b]8;;https://evil.example\x1b\\link\x1b]8;;\x1b\\",
					State:    "errored",
					Signal:   "SIG\tKILL\n\x1b[2J",
					ExitCode: &exitOne,
				},
				{ID: "efgh2345", Name: "spoof", Agent: "claude", State: "working"},
				{ID: "ijkl2345", Name: "spoof\u202e\u200b", Agent: "codex", State: "lost", Reason: "reboot\t\r\n\x00\x7f"},
			},
		},
		Seen: map[string]time.Time{"box": now.Add(-10 * time.Second)},
	}
	b, err := json.Marshal(cache)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "launcher.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}

	assertClean := func(items []string) {
		t.Helper()
		for _, line := range items {
			if strings.Count(line, "\t") > 1 {
				t.Errorf("line has more than one tab (corrupts Cobra protocol): %q", line)
			}
			for _, bad := range []string{"[31m", "[32m", "[2J", "52;c;", "evil.example"} {
				if strings.Contains(line, bad) {
					t.Errorf("line leaked escape payload %q: %q", bad, line)
				}
			}
			for _, r := range line {
				if r == '\t' {
					continue
				}
				if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) || (r >= 0x200b && r <= 0x200f) {
					t.Errorf("line contains unsafe rune U+%04X: %q", r, line)
				}
			}
		}
	}

	hostItems, dir, _ := execComplete(t, nil, cobra.ShellCompRequestCmd, "host", "show", "")
	if dir != cobra.ShellCompDirectiveNoFileComp || len(hostItems) != 1 {
		t.Fatalf("host items = %v, dir = %d", hostItems, dir)
	}
	assertClean(hostItems)

	sessItems, dir, _ := execComplete(t, nil, cobra.ShellCompRequestCmd, "attach", "")
	if dir != cobra.ShellCompDirectiveNoFileComp || len(sessItems) != 3 {
		t.Fatalf("session items = %v, dir = %d", sessItems, dir)
	}
	assertClean(sessItems)
	// An exact safe name remains usable. A changed name falls back to its ID.
	vals := make([]string, len(sessItems))
	for i, it := range sessItems {
		vals[i], _, _ = strings.Cut(it, "\t")
	}
	for _, want := range []string{"box/abcd2345", "box/spoof", "box/ijkl2345"} {
		if !slices.Contains(vals, want) {
			t.Errorf("values %v missing %q", vals, want)
		}
	}
}

// Missing, corrupt, oversized, unreadable, directory or FIFO files never fail,
// panic, block or offer local file names (cli-spec 14.3, rule 4).
func TestCompletionCorruptAndUnreadableFiles(t *testing.T) {
	cfgDir, stateDir := isolateCompletionEnv(t)

	checkEmpty := func(label string) {
		t.Helper()
		for _, q := range [][]string{{"host", "show", ""}, {"attach", ""}, {"new", "box", "--secret", ""}} {
			items, dir, errOut := execComplete(t, nil, cobra.ShellCompRequestCmd, q...)
			if len(items) != 0 || dir != cobra.ShellCompDirectiveNoFileComp || errOut != "" {
				t.Fatalf("%s (%v): items=%v dir=%d stderr=%q", label, q, items, dir, errOut)
			}
		}
	}

	// 1. Missing files.
	checkEmpty("missing")

	// 2. Syntax errors in TOML and JSON.
	for path, bad := range map[string]string{
		filepath.Join(cfgDir, "hosts.toml"):      "[[host]\nname = ",
		filepath.Join(cfgDir, "config.toml"):     "[secrets\nX = 1",
		filepath.Join(stateDir, "launcher.json"): "{not json",
	} {
		if err := os.WriteFile(path, []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	checkEmpty("corrupt")

	// 3. Broken hosts.toml still allows cached session completion from launcher.json.
	validCache := `{"sessions":{"box":[{"id":"abcd2345","name":"ok","agent":"claude","state":"working"}]}}`
	if err := os.WriteFile(filepath.Join(stateDir, "launcher.json"), []byte(validCache), 0o600); err != nil {
		t.Fatal(err)
	}
	items, dir, _ := execComplete(t, nil, cobra.ShellCompRequestCmd, "attach", "")
	if dir != cobra.ShellCompDirectiveNoFileComp || len(items) != 1 || !strings.HasPrefix(items[0], "box/ok\t") {
		t.Fatalf("broken hosts.toml blocked session cache: items=%v dir=%d", items, dir)
	}

	// 4. Oversized files (> maxCompletionBytes).
	big := bytes.Repeat([]byte("a"), maxCompletionBytes+1024)
	for _, p := range []string{filepath.Join(cfgDir, "hosts.toml"), filepath.Join(cfgDir, "config.toml"), filepath.Join(stateDir, "launcher.json")} {
		if err := os.WriteFile(p, big, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	checkEmpty("oversized")

	// 5. Directories where files should be.
	for _, p := range []string{filepath.Join(cfgDir, "hosts.toml"), filepath.Join(cfgDir, "config.toml"), filepath.Join(stateDir, "launcher.json")} {
		_ = os.Remove(p)
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	checkEmpty("directory")

	// 6. Unreadable files and FIFOs on Unix.
	if runtime.GOOS != "windows" {
		for _, p := range []string{filepath.Join(cfgDir, "hosts.toml"), filepath.Join(cfgDir, "config.toml"), filepath.Join(stateDir, "launcher.json")} {
			_ = os.RemoveAll(p)
			if err := syscall.Mkfifo(p, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			checkEmpty("fifo")
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("completion blocked on a FIFO")
		}

		if os.Geteuid() != 0 {
			for _, p := range []string{filepath.Join(cfgDir, "hosts.toml"), filepath.Join(cfgDir, "config.toml"), filepath.Join(stateDir, "launcher.json")} {
				_ = os.RemoveAll(p)
				if err := os.WriteFile(p, []byte("x"), 0o000); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(p, 0o600) })
			}
			checkEmpty("unreadable")
		}
	}
}

// Old names never appear in completion and write nothing to stderr, even when
// diag.EnvFile is set (cli-spec 14.3 rules 6 & 7, 14.4).
func TestCompletionOldNamesSilent(t *testing.T) {
	cfgDir, stateDir := isolateCompletionEnv(t)
	writeCompletionFixtures(t, cfgDir, stateDir, time.Now())
	dbgFile := filepath.Join(t.TempDir(), "debug.jsonl")
	t.Setenv(diag.EnvFile, dbgFile)

	rootItems, _, errOut := execComplete(t, nil, cobra.ShellCompRequestCmd, "")
	if errOut != "" {
		t.Fatalf("root completion stderr = %q", errOut)
	}
	for _, it := range rootItems {
		name, _, _ := strings.Cut(it, "\t")
		if name == "kill" || name == "flip" {
			t.Errorf("root completion offered old command %q", name)
		}
	}
	hostItems, _, errOut := execComplete(t, nil, cobra.ShellCompRequestCmd, "host", "")
	if errOut != "" {
		t.Fatalf("host completion stderr = %q", errOut)
	}
	for _, it := range hostItems {
		name, _, _ := strings.Cut(it, "\t")
		if name == "destroy" || name == "idle" {
			t.Errorf("host completion offered old command %q", name)
		}
	}
	for _, old := range [][]string{{"kill", ""}, {"flip", ""}, {"host", "destroy", ""}, {"host", "idle", ""}} {
		items, dir, errOut := execComplete(t, nil, cobra.ShellCompRequestCmd, old...)
		if errOut != "" {
			t.Errorf("__complete %v wrote to stderr: %q", old, errOut)
		}
		if len(items) != 0 || dir != cobra.ShellCompDirectiveNoFileComp {
			t.Errorf("__complete %v = %v (dir %d), want empty NoFileComp", old, items, dir)
		}
	}
	if _, err := os.Stat(dbgFile); err == nil {
		t.Errorf("__complete created debug file %s", dbgFile)
	}
}

// Structural walk of Root(): every command taking a host, session, agent or
// provider has ValidArgsFunction wired, non-file flags use NoFileComp, and
// local file flags use ShellCompDirectiveDefault.
func TestCompletionCommandTreeWiring(t *testing.T) {
	isolateCompletionEnv(t)
	root := Root()
	if root.CompletionOptions.DefaultShellCompDirective == nil || *root.CompletionOptions.DefaultShellCompDirective != cobra.ShellCompDirectiveNoFileComp {
		t.Fatalf("DefaultShellCompDirective = %v, want NoFileComp", root.CompletionOptions.DefaultShellCompDirective)
	}

	// Verify all four shell scripts generate cleanly via Root().
	for _, sh := range []string{"bash", "zsh", "fish", "powershell"} {
		out, errOut, err := runApart(t, "completion", sh)
		if err != nil || errOut != "" || len(out) < 100 {
			t.Fatalf("completion %s: err=%v len=%d stderr=%q", sh, err, len(out), errOut)
		}
	}

	localFileFlags := map[string]bool{"debug-file": true, "ssh-config": true, "provider-script": true}
	valueFlags := map[string]bool{
		"host": true, "agent": true, "agents": true, "create": true, "secret": true,
		"debug": true, "only": true, "memory-max": true, "idle-stop": true,
		"format": true, "events": true, "event": true,
	}

	var walk func(c *cobra.Command, path []string)
	walk = func(c *cobra.Command, path []string) {
		if c.Hidden {
			return
		}
		use := c.Use
		needsArgComp := strings.Contains(use, "<host>") ||
			strings.Contains(use, "<host>/<session>") ||
			strings.Contains(use, "<agent>") ||
			strings.Contains(use, "<provider>") ||
			(len(path) >= 2 && path[0] == "host" && path[1] != "add" && strings.Contains(use, "<name>"))
		if c.Runnable() && needsArgComp {
			if c.ValidArgsFunction == nil {
				t.Errorf("%s (%q) is missing ValidArgsFunction", strings.Join(path, " "), use)
			}
			_, dir, errOut := execComplete(t, nil, cobra.ShellCompRequestCmd, append(append([]string{}, path...), "")...)
			if dir&cobra.ShellCompDirectiveNoFileComp == 0 {
				t.Errorf("%s (%q) allows file fallback (directive %d)", strings.Join(path, " "), use, dir)
			}
			if errOut != "" {
				t.Errorf("%s wrote to stderr: %q", strings.Join(path, " "), errOut)
			}
		}
		c.NonInheritedFlags().VisitAll(func(f *pflag.Flag) {
			if f.Hidden || f.Deprecated != "" || f.Value.Type() == "bool" {
				return
			}
			var q []string
			if f.NoOptDefVal != "" {
				q = append(append([]string{}, path...), "--"+f.Name+"=")
			} else {
				q = append(append([]string{}, path...), "--"+f.Name, "")
			}
			_, dir, _ := execComplete(t, nil, cobra.ShellCompRequestCmd, q...)
			if localFileFlags[f.Name] {
				if dir != cobra.ShellCompDirectiveDefault {
					t.Errorf("%s --%s directive = %d, want Default (local file)", strings.Join(path, " "), f.Name, dir)
				}
			} else if valueFlags[f.Name] || dir != cobra.ShellCompDirectiveNoFileComp {
				if dir&cobra.ShellCompDirectiveNoFileComp == 0 {
					t.Errorf("%s --%s directive = %d, missing NoFileComp", strings.Join(path, " "), f.Name, dir)
				}
			}
		})
		for _, sub := range c.Commands() {
			walk(sub, append(append([]string{}, path...), sub.Name()))
		}
	}
	walk(root, nil)
}

// Benchmark session completion with 20 hosts and 1,000 cached sessions
// (cli-spec 14.4). Records timing and allocations without gating CI.
func BenchmarkCompleteSessions(b *testing.B) {
	base := b.TempDir()
	b.Setenv("HOME", base)
	b.Setenv("USERPROFILE", base)
	b.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "config"))
	b.Setenv("XDG_STATE_HOME", filepath.Join(base, "state"))
	cfgDir := filepath.Join(base, "config", brand.Name)
	stateDir := filepath.Join(base, "state", brand.Name)
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		b.Fatal(err)
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		b.Fatal(err)
	}

	var hostsBuf strings.Builder
	cache := client.SessionCache{
		Sessions: make(map[string][]client.Session, 20),
		Seen:     make(map[string]time.Time, 20),
	}
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	prevNow := compNow
	compNow = func() time.Time { return now }
	b.Cleanup(func() { compNow = prevNow })

	for h := range 20 {
		hname := fmt.Sprintf("host-%02d", h)
		fmt.Fprintf(&hostsBuf, "[[host]]\nname = %q\nssh = %q\n\n", hname, "dev@"+hname+".example")
		list := make([]client.Session, 50)
		for s := range 50 {
			list[s] = client.Session{
				ID:    fmt.Sprintf("ab%02d%02d23", h, s),
				Name:  fmt.Sprintf("task-%02d", s),
				Agent: "claude",
				State: "working",
			}
		}
		cache.Sessions[hname] = list
		cache.Seen[hname] = now.Add(-2 * time.Minute)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "hosts.toml"), []byte(hostsBuf.String()), 0o600); err != nil {
		b.Fatal(err)
	}
	raw, err := json.Marshal(cache)
	if err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "launcher.json"), raw, 0o600); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		items, _ := completeSessions(nil, nil, "")
		if len(items) != 1000 {
			b.Fatalf("got %d items, want 1000", len(items))
		}
	}
}
