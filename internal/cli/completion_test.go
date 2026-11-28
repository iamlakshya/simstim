package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/diag"
)

func completionFixture(t *testing.T) (configDir, stateDir string) {
	t.Helper()
	base := t.TempDir()
	xdgConfig := filepath.Join(base, "config")
	xdgState := filepath.Join(base, "state")
	xdgCache := filepath.Join(base, "cache")
	t.Setenv("XDG_CONFIG_HOME", xdgConfig)
	t.Setenv("XDG_STATE_HOME", xdgState)
	t.Setenv("XDG_CACHE_HOME", xdgCache)
	t.Setenv("NO_COLOR", "1")

	fixedNow := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	prevNow := completionNow
	completionNow = func() time.Time { return fixedNow }
	t.Cleanup(func() { completionNow = prevNow })

	configDir = filepath.Join(xdgConfig, brand.Name)
	stateDir = filepath.Join(xdgState, brand.Name)
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}

	hosts := []client.Host{
		{Name: "atlas", SSH: "dev@atlas.example.com"},
		{Name: "box", SSH: "box-ssh"},
		{Name: "lima", Machine: &client.Machine{Provider: "lima", ID: brand.Name + "-lima"}},
	}
	if err := client.SaveHosts(context.Background(), hosts); err != nil {
		t.Fatal(err)
	}

	cfg := "[secrets]\nANTHROPIC_API_KEY = \"exit 99\"\nOPENAI_API_KEY = \"exit 99\"\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	exitOne := 1
	cache := client.SessionCache{
		Sessions: map[string][]client.Session{
			"atlas": {
				{ID: "abcd2345", Name: "auth-fix", Agent: "claude", State: api.StateWorking, Alive: true},
				{ID: "efgh6789", Name: "api", Agent: "codex", State: api.StateIdle, Alive: true},
			},
			"box": {
				{ID: "ijkl2345", Name: "dup", Agent: "claude", State: api.StateNeedsInput, Alive: true},
				{ID: "mnop6789", Name: "dup", Agent: "codex", State: api.StateErrored, ExitCode: &exitOne},
				{ID: "qrst2345", Name: "", Agent: "shell", State: api.StateIdle, Alive: true},
				{ID: "uvwx2345", Name: "bad name\x1b[31m", Agent: "claude", State: api.StateWorking, Alive: true},
			},
		},
		Seen: map[string]time.Time{
			"atlas": fixedNow.Add(-10 * time.Second),
			"box":   fixedNow.Add(-5 * time.Minute),
		},
	}
	raw, err := json.Marshal(cache)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "launcher.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return configDir, stateDir
}

func isQuietCompStderr(errOut string) bool {
	for _, l := range strings.Split(strings.TrimSpace(errOut), "\n") {
		if l != "" && !strings.HasPrefix(l, "Completion ended with directive:") {
			return false
		}
	}
	return true
}

// Golden tests of __complete for every row of cli-spec 14.2 present in this
// build, using fixture XDG_CONFIG_HOME and XDG_STATE_HOME folders.
func TestGoldenCompletion(t *testing.T) {
	completionFixture(t)

	cases := []struct {
		name string
		args []string
	}{
		{"host positional (new)", []string{"new", ""}},
		{"host positional prefix (new a)", []string{"new", "a"}},
		{"host positional (ls)", []string{"ls", ""}},
		{"host positional (host show)", []string{"host", "show", ""}},
		{"host positional with subcommands (host alerts)", []string{"host", "alerts", ""}},
		{"host positional (image pull arg 1)", []string{"image", "pull", "default", ""}},
		{"host flag (ls --host)", []string{"ls", "--host", ""}},
		{"host flag (agents install --host)", []string{"agents", "install", "claude", "--host", ""}},
		{"host flag (setup --host)", []string{"setup", "--host", ""}},
		{"session address (attach)", []string{"attach", ""}},
		{"session address filtered by host (attach box/)", []string{"attach", "box/"}},
		{"session address (show)", []string{"show", ""}},
		{"session address (debug profile arg 0)", []string{"debug", "profile", ""}},
		{"debug profile kind (arg 1)", []string{"debug", "profile", "atlas/auth-fix", ""}},
		{"host or session (debug bundle)", []string{"debug", "bundle", ""}},
		{"host or session filtered (debug bundle atlas/)", []string{"debug", "bundle", "atlas/"}},
		{"cp arg 0 remote or local", []string{"cp", ""}},
		{"cp arg 1 after local source", []string{"cp", "./file.txt", ""}},
		{"cp arg 1 after remote source", []string{"cp", "atlas/auth-fix:notes.md", ""}},
		{"agent flag (new --agent)", []string{"new", "--agent", ""}},
		{"agents install arg 0", []string{"agents", "install", ""}},
		{"agents install arg 1 host", []string{"agents", "install", "claude", ""}},
		{"agents login arg 0", []string{"agents", "login", ""}},
		{"setup --agents empty", []string{"setup", "--agents", ""}},
		{"setup --agents after comma", []string{"setup", "--agents", "claude,"}},
		{"host set keys", []string{"host", "set", "atlas", ""}},
		{"host set key prefix", []string{"host", "set", "atlas", "f"}},
		{"host set idle value", []string{"host", "set", "atlas", "idle="}},
		{"host set floor value", []string{"host", "set", "atlas", "floor="}},
		{"host set trusted value", []string{"host", "set", "atlas", "trusted="}},
		{"host set label value", []string{"host", "set", "atlas", "label.env="}},
		{"host rm --confirm", []string{"host", "rm", "atlas", "--destroy", "--confirm", ""}},
		{"wall flag (new --wall)", []string{"new", "--wall", ""}},
		{"floor flag (host add --floor)", []string{"host", "add", "--floor", ""}},
		{"autostart flag (host create --autostart)", []string{"host", "create", "lima", "--autostart", ""}},
		{"provider arg (host create)", []string{"host", "create", ""}},
		{"provider flag (setup --create)", []string{"setup", "--create", ""}},
		{"secret flag (new --secret)", []string{"new", "--secret", ""}},
		{"expose flag (new --expose)", []string{"new", "--expose", ""}},
		{"alert format flag", []string{"host", "alerts", "set", "atlas", "--format", ""}},
		{"alert events flag after comma", []string{"host", "alerts", "set", "atlas", "--events", "failed,"}},
		{"alert test event flag", []string{"host", "alerts", "test", "atlas", "--event", ""}},
		{"image pull ref (arg 0)", []string{"image", "pull", ""}},
		{"debug flag categories", []string{"--debug="}},
		{"debug flag categories after comma", []string{"--debug=net,"}},
		{"debug start --only", []string{"debug", "start", "atlas/auth-fix", "--only", ""}},
		{"completion shells", []string{"completion", ""}},
		{"host subcommands (no old names)", []string{"host", ""}},
	}

	var b strings.Builder
	for _, tc := range cases {
		out, errOut, err := runApart(t, append([]string{cobra.ShellCompRequestCmd}, tc.args...)...)
		if err != nil {
			t.Fatalf("%s: __complete %v failed: %v\nstderr: %s", tc.name, tc.args, err, errOut)
		}
		if !isQuietCompStderr(errOut) {
			t.Errorf("%s: unexpected stderr: %q", tc.name, errOut)
		}
		fmt.Fprintf(&b, "=== %s ===\n%s\n", tc.name, out)
	}

	golden := filepath.Join("testdata", "completion.golden")
	got := b.String()
	if *update {
		if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read %s: %v (run go test ./internal/cli -run TestGoldenCompletion -update)", golden, err)
	}
	if got != string(want) {
		t.Errorf("completion output differs from %s (run go test ./internal/cli -run TestGoldenCompletion -update, and read the diff):\n%s", golden, got)
	}
}

// Missing or broken hosts.toml, launcher.json and config.toml produce no
// candidates with ShellCompDirectiveNoFileComp, never ShellCompDirectiveError,
// and write no warnings to stderr (cli-spec 14.3, rules 3 and 4).
func TestCompletionBrokenAndMissingFiles(t *testing.T) {
	noFile := fmt.Sprintf(":%d\n", cobra.ShellCompDirectiveNoFileComp)
	errDir := fmt.Sprintf(":%d\n", cobra.ShellCompDirectiveError)

	for _, mode := range []string{"missing", "broken"} {
		t.Run(mode, func(t *testing.T) {
			base := t.TempDir()
			xdgConfig := filepath.Join(base, "config")
			xdgState := filepath.Join(base, "state")
			t.Setenv("XDG_CONFIG_HOME", xdgConfig)
			t.Setenv("XDG_STATE_HOME", xdgState)
			t.Setenv("XDG_CACHE_HOME", filepath.Join(base, "cache"))
			t.Setenv("NO_COLOR", "1")

			if mode == "broken" {
				cfgDir := filepath.Join(xdgConfig, brand.Name)
				stDir := filepath.Join(xdgState, brand.Name)
				if err := os.MkdirAll(cfgDir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(stDir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(cfgDir, "hosts.toml"), []byte("[[host]\nbroken ="), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte("[broken\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(stDir, "launcher.json"), []byte("{not valid json"), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			for _, args := range [][]string{
				{"new", ""},
				{"ls", "--host", ""},
				{"attach", ""},
				{"new", "--secret", ""},
				{"debug", "bundle", ""},
			} {
				out, errOut, err := runApart(t, append([]string{cobra.ShellCompRequestCmd}, args...)...)
				if err != nil {
					t.Fatalf("%v: %v", args, err)
				}
				if out != noFile {
					t.Errorf("%v: stdout = %q, want %q", args, out, noFile)
				}
				if strings.Contains(out, errDir) {
					t.Errorf("%v: returned ShellCompDirectiveError: %q", args, out)
				}
				if !isQuietCompStderr(errOut) {
					t.Errorf("%v: unexpected stderr: %q", args, errOut)
				}
			}

			// Generating a shell completion script must also stay quiet when
			// config.toml is broken.
			out, errOut, err := runApart(t, "completion", "zsh")
			if err != nil || out == "" {
				t.Fatalf("completion zsh: err=%v len=%d", err, len(out))
			}
			if errOut != "" {
				t.Errorf("completion zsh stderr with %s config: %q", mode, errOut)
			}
		})
	}
}

// Completion never dials a host, invokes a provider, runs a secret command or
// writes the session cache (cli-spec 14.3 rules 1-2, 14.4).
func TestCompletionNeverDialsOrWritesCache(t *testing.T) {
	configDir, stateDir := completionFixture(t)
	marker := filepath.Join(t.TempDir(), "touched")

	// Failing provider registered for a machine host.
	const provName = "fail-comp-dial-test"
	client.RegisterProvider(provName, func(client.Host) (client.Lifecycle, error) {
		t.Fatal("completion opened machine provider")
		return nil, nil
	})

	// Failing provider script and failing ssh binary on PATH.
	binDir := t.TempDir()
	failScript := fmt.Sprintf("#!/bin/sh\necho called > %q\nexit 99\n", marker)
	sshPath := filepath.Join(binDir, "ssh")
	provScript := filepath.Join(binDir, "prov.sh")
	for _, p := range []string{sshPath, provScript} {
		if err := os.WriteFile(p, []byte(failScript), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	hosts := []client.Host{
		{Name: "ssh-host", SSH: "dev@ssh.example.invalid"},
		{Name: "script-host", SSH: "dev@script.example.invalid", Provider: &client.Provider{Script: provScript, WakeOnAttach: true}},
		{Name: "machine-host", Machine: &client.Machine{Provider: provName, ID: "m1"}},
	}
	if err := client.SaveHosts(context.Background(), hosts); err != nil {
		t.Fatal(err)
	}
	secretCfg := fmt.Sprintf("[secrets]\nAPI_KEY = \"echo called > %s; exit 99\"\n", marker)
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(secretCfg), 0o600); err != nil {
		t.Fatal(err)
	}

	prevList := listSessions
	listSessions = func(context.Context, *client.Conn) ([]client.Session, error) {
		t.Fatal("completion called listSessions")
		return nil, nil
	}
	t.Cleanup(func() { listSessions = prevList })

	cacheFile := filepath.Join(stateDir, "launcher.json")
	beforeBytes, err := os.ReadFile(cacheFile)
	if err != nil {
		t.Fatal(err)
	}
	oldTime := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := os.Chtimes(cacheFile, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{
		{"new", ""},
		{"new", "--secret", ""},
		{"attach", ""},
		{"host", "show", ""},
		{"host", "set", "machine-host", ""},
		{"debug", "bundle", ""},
	} {
		if _, errOut, err := runApart(t, append([]string{cobra.ShellCompRequestCmd}, args...)...); err != nil || !isQuietCompStderr(errOut) {
			t.Fatalf("%v: err=%v stderr=%q", args, err, errOut)
		}
	}

	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("completion invoked ssh, a provider script or a secret command")
	}
	fi, err := os.Stat(cacheFile)
	if err != nil {
		t.Fatal(err)
	}
	if !fi.ModTime().Equal(oldTime) {
		t.Errorf("launcher.json mtime changed: got %v, want %v", fi.ModTime(), oldTime)
	}
	afterBytes, err := os.ReadFile(cacheFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterBytes) != string(beforeBytes) {
		t.Error("launcher.json contents changed during completion")
	}
}

// Old names of cli-spec section 3 never complete, and passing an old name to
// __complete prints nothing of its own on stderr (cli-spec 14.3 rule 6, 14.4).
func TestCompletionOldNamesNeverCompleteAndStayQuiet(t *testing.T) {
	configDir, _ := completionFixture(t)
	t.Setenv(diag.EnvFile, filepath.Join(t.TempDir(), "debug.jsonl"))
	t.Cleanup(func() { client.StopDebug(io.Discard) })
	// Also corrupt config.toml to confirm no warning leaks to stderr.
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte("[broken\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Old commands and flags must not appear in completions.
	checkAbsent := func(args []string, forbidden ...string) {
		t.Helper()
		out, errOut, err := runApart(t, append([]string{cobra.ShellCompRequestCmd}, args...)...)
		if err != nil || !isQuietCompStderr(errOut) {
			t.Fatalf("%v: err=%v stderr=%q", args, err, errOut)
		}
		for _, line := range strings.Split(out, "\n") {
			word, _, _ := strings.Cut(line, "\t")
			for _, bad := range forbidden {
				if word == bad {
					t.Errorf("%v offered hidden old name %q:\n%s", args, bad, out)
				}
			}
		}
	}
	checkAbsent([]string{""}, "kill", "flip", "ssh-proxy")
	checkAbsent([]string{"host", ""}, "idle", "destroy")
	checkAbsent([]string{"agents", "install", "--"}, "--force")

	// Requesting completion on an old command or flag must stay quiet on stderr.
	for _, args := range [][]string{
		{"kill", ""},
		{"flip", ""},
		{"host", "idle", ""},
		{"host", "destroy", ""},
		{"agents", "install", "--force", ""},
	} {
		_, errOut, err := runApart(t, append([]string{cobra.ShellCompRequestCmd}, args...)...)
		if err != nil {
			t.Fatalf("__complete %v: %v", args, err)
		}
		if !isQuietCompStderr(errOut) {
			t.Errorf("__complete %v wrote extra stderr: %q", args, errOut)
		}
	}
}

// Every visible runnable command has completion wired, and every command path
// referenced by wireCompletions exists so renames cannot silently drop wiring.
func TestEveryVisibleCommandHasCompletion(t *testing.T) {
	root := Root()
	if missing := verifyCompletionBindings(root); len(missing) > 0 {
		t.Errorf("completion wiring references missing targets: %v", missing)
	}
	eachCommand(root, func(c *cobra.Command) {
		if !c.HasParent() || c.Hidden || IsTopic(c) || !c.Runnable() {
			return
		}
		if c.Name() == "help" || c.Name() == "completion" {
			return
		}
		if c.Parent() != nil && c.Parent().Name() == "completion" {
			return
		}
		if c.ValidArgsFunction == nil {
			t.Errorf("%s has no ValidArgsFunction", c.CommandPath())
		}
	})
}

// BenchmarkCompleteSessions measures session completion with 20 hosts and
// 1,000 cached sessions (50 per host; cli-spec 14.4).
func BenchmarkCompleteSessions(b *testing.B) {
	base := b.TempDir()
	b.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "config"))
	b.Setenv("XDG_STATE_HOME", filepath.Join(base, "state"))
	b.Setenv("XDG_CACHE_HOME", filepath.Join(base, "cache"))

	stateDir := filepath.Join(base, "state", brand.Name)
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		b.Fatal(err)
	}
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	prevNow := completionNow
	completionNow = func() time.Time { return now }
	b.Cleanup(func() { completionNow = prevNow })

	cache := client.SessionCache{
		Sessions: make(map[string][]client.Session, 20),
		Seen:     make(map[string]time.Time, 20),
	}
	for h := range 20 {
		hostName := fmt.Sprintf("host-%02d", h+1)
		list := make([]client.Session, 50)
		for i := range 50 {
			name := fmt.Sprintf("task-%02d", i+1)
			if i < 2 {
				name = "shared-name"
			}
			list[i] = client.Session{
				ID:    fmt.Sprintf("ab%02d%04d", h+1, i+1),
				Name:  name,
				Agent: "claude",
				State: api.StateWorking,
				Alive: true,
			}
		}
		cache.Sessions[hostName] = list
		if h%2 == 0 {
			cache.Seen[hostName] = now.Add(-15 * time.Second)
		} else {
			cache.Seen[hostName] = now.Add(-5 * time.Minute)
		}
	}
	raw, err := json.Marshal(cache)
	if err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "launcher.json"), raw, 0o600); err != nil {
		b.Fatal(err)
	}

	b.Run("all-1000", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			got, dir := completeSessions(nil, nil, "")
			if len(got) != 1000 || dir != cobra.ShellCompDirectiveNoFileComp {
				b.Fatalf("got %d entries, dir=%v", len(got), dir)
			}
		}
	})
	b.Run("prefix-one-host", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			got, dir := completeSessions(nil, nil, "host-10/")
			if len(got) != 50 || dir != cobra.ShellCompDirectiveNoFileComp {
				b.Fatalf("got %d entries, dir=%v", len(got), dir)
			}
		}
	})
}
