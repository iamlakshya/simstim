package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/provider"
)

// Every short flag has one meaning everywhere, and belongs to the allowlist of
// docs/contributing/cli-spec.md 10.1. InitDefaultVersionFlag runs before the
// walk so that Cobra cannot silently re-add -v to the root.
func TestShortFlags(t *testing.T) {
	allowed := map[string]string{
		"a": "agent",
		"C": "dir",
		"d": "detach",
		"f": "force",
		"h": "help",
		"n": "name",
		"r": "recursive",
		"w": "worktree",
		"y": "yes",
	}
	checkTree := func(t *testing.T, root *cobra.Command) {
		t.Helper()
		root.InitDefaultVersionFlag()
		seen := map[string]string{}
		eachCommand(root, func(c *cobra.Command) {
			c.InitDefaultHelpFlag()
			visit := func(f *pflag.Flag) {
				if f.Shorthand == "" {
					return
				}
				if f.Hidden && isOldFlag(f) {
					return
				}
				want, ok := allowed[f.Shorthand]
				if !ok {
					t.Errorf("%s: short flag -%s (--%s) is not in the table of cli-spec 10.1", c.CommandPath(), f.Shorthand, f.Name)
					return
				}
				if f.Name != want {
					t.Errorf("%s: -%s names --%s, want --%s", c.CommandPath(), f.Shorthand, f.Name, want)
				}
				if prev, dup := seen[f.Shorthand]; dup && prev != f.Name {
					t.Errorf("%s: -%s means both --%s and --%s", c.CommandPath(), f.Shorthand, prev, f.Name)
				}
				seen[f.Shorthand] = f.Name
			}
			c.LocalFlags().VisitAll(visit)
			c.PersistentFlags().VisitAll(visit)
		})
	}
	t.Run("current tree", func(t *testing.T) {
		checkTree(t, Root())
	})
	t.Run("after new -d ends", func(t *testing.T) {
		old := oldNewShortDir
		oldNewShortDir = nil
		t.Cleanup(func() { oldNewShortDir = old })
		root := Root()
		checkTree(t, root)
		for _, name := range []string{"new", "resume"} {
			c, _, err := root.Find([]string{name})
			if err != nil {
				t.Fatal(err)
			}
			if f := c.Flags().Lookup("detach"); f == nil || f.Shorthand != "d" {
				t.Errorf("%s --detach shorthand after end = %+v, want -d", name, f)
			}
		}
	})
}

// Every flag in the shared-flags table of docs/contributing/cli-spec.md 10.2
// must be registered through its helper in internal/cli/flags.go.
func TestSharedFlagHelpers(t *testing.T) {
	shared := map[string]bool{
		"yes":           true,
		"force":         true,
		"confirm":       true,
		"json":          true,
		"dry-run":       true,
		"no-input":      true,
		"no-color":      true,
		"debug":         true,
		"debug-file":    true,
		"name":          true,
		"host":          true,
		"detach":        true,
		"memory-max":    true,
		"memory":        true,
		"cpus":          true,
		"secret":        true,
		"for":           true,
		"all":           true,
		"follow":        true,
		"fix":           true,
		"label":         true,
		"token-command": true,
		"root-step":     true,
		"images":        true,
		"profile":       true,
		"in":            true,
		"count":         true,
	}
	root := Root()
	eachCommand(root, func(c *cobra.Command) {
		check := func(f *pflag.Flag) {
			if f.Hidden && isOldFlag(f) {
				return
			}
			if shared[f.Name] && !isSharedFlag(f) {
				t.Errorf("%s: --%s must be defined with its helper in internal/cli/flags.go", c.CommandPath(), f.Name)
			}
			switch f.Name {
			case "yes":
				if f.Shorthand != "y" {
					t.Errorf("%s: --yes must have shorthand -y", c.CommandPath())
				}
			case "name":
				if f.Shorthand != "n" {
					t.Errorf("%s: --name must have shorthand -n", c.CommandPath())
				}
			case "force":
				want := ""
				if shortPath(c) == "cp" {
					want = "f"
				}
				if f.Shorthand != want {
					t.Errorf("%s: --force shorthand = %q, want %q", c.CommandPath(), f.Shorthand, want)
				}
			}
		}
		c.LocalFlags().VisitAll(check)
		c.PersistentFlags().VisitAll(check)
	})
}

func TestFlagParsers(t *testing.T) {
	t.Run("lists", func(t *testing.T) {
		got, err := parseNameList("--events", []string{"failed, oom", "started"}, false, "")
		if err != nil || !slices.Equal(got, []string{"failed", "oom", "started"}) {
			t.Fatalf("parseNameList = %v, %v", got, err)
		}
		for _, empty := range [][]string{{""}, {","}, {" , , "}, nil} {
			if _, err := parseNameList("--events", empty, false, "Name an event."); err == nil {
				t.Errorf("parseNameList(%q, allowEmpty=false) succeeded", empty)
			} else {
				var ue *client.UserError
				if !errors.As(err, &ue) || ue.Kind != client.KindUsage || ue.Hint != "Name an event." {
					t.Errorf("parseNameList(%q) error = %+v", empty, err)
				}
			}
			if got, err := parseNameList("--only", empty, true, ""); err != nil || len(got) != 0 {
				t.Errorf("parseNameList(%q, allowEmpty=true) = %v, %v", empty, got, err)
			}
		}
	})

	t.Run("sizes", func(t *testing.T) {
		for in, want := range map[string]int64{
			"1K":   1 << 10,
			"512M": 512 << 20,
			"1.5G": 1536 << 20,
			"4GiB": 4 << 30,
			"1T":   1 << 40,
		} {
			got, err := parseSizeFlag("--memory", in)
			if err != nil || got != want {
				t.Errorf("parseSizeFlag(%q) = %d, %v, want %d", in, got, err, want)
			}
		}
		for _, bad := range []string{"", "4", "1.5", "0G", "0.0001K", "-1G", "4X", "10000000T"} {
			_, err := parseSizeFlag("--memory", bad)
			var ue *client.UserError
			if !errors.As(err, &ue) || ue.Kind != client.KindUsage {
				t.Errorf("parseSizeFlag(%q) = %v, want KindUsage", bad, err)
			}
		}
	})

	t.Run("legacy GiB sizes", func(t *testing.T) {
		t.Setenv("NO_COLOR", "1")
		for in, want := range map[string]int64{
			"8":    8 << 30,
			"010":  8 << 30,
			"0x10": 16 << 30,
			"0":    0,
			"-4":   0,
		} {
			root := Root()
			cmd, _, _ := root.Find([]string{"host", "create"})
			var got int64
			var err error
			_, errOut := captureOutput(t, func() {
				got, err = parseLegacyGiBSizeFlag(cmd, "--memory", in, oldCreateMemoryBare)
			})
			if err != nil || got != want {
				t.Errorf("parseLegacyGiBSizeFlag(%q) = %d, %v, want %d", in, got, err, want)
			}
			if strings.Count(errOut, "Warning: ") != 1 {
				t.Errorf("parseLegacyGiBSizeFlag(%q) warnings = %q, want 1", in, errOut)
			}
		}
		// Sized values do not warn.
		root := Root()
		cmd, _, _ := root.Find([]string{"host", "create"})
		var got int64
		var err error
		_, errOut := captureOutput(t, func() {
			got, err = parseLegacyGiBSizeFlag(cmd, "--memory", "1.5G", oldCreateMemoryBare)
		})
		if err != nil || got != 1536<<20 || errOut != "" {
			t.Errorf("parseLegacyGiBSizeFlag(1.5G) = %d, %v, stderr %q", got, err, errOut)
		}
		// Bare floats, bad units, and overflows fail with exit code 2.
		for _, bad := range []string{"1.5", "lots", "999999999999999999999", "4294967296"} {
			root = Root()
			cmd, _, _ = root.Find([]string{"host", "create"})
			_, err := parseLegacyGiBSizeFlag(cmd, "--memory", bad, oldCreateMemoryBare)
			var ue *client.UserError
			if !errors.As(err, &ue) || ue.Kind != client.KindUsage {
				t.Errorf("parseLegacyGiBSizeFlag(%q) = %v, want KindUsage", bad, err)
			}
		}
		// After the old form ends (old == nil), bare integers fail with exit code 2.
		_, err = parseLegacyGiBSizeFlag(cmd, "--memory", "8", nil)
		var ue *client.UserError
		if !errors.As(err, &ue) || ue.Kind != client.KindUsage {
			t.Errorf("parseLegacyGiBSizeFlag(8, nil) = %v, want KindUsage", err)
		}
	})

	t.Run("durations", func(t *testing.T) {
		if d, never, err := parseDurationFlag("--idle-stop", "", true, time.Minute); err != nil || d != 0 || never {
			t.Errorf("empty = %v, %v, %v", d, never, err)
		}
		if d, never, err := parseDurationFlag("--idle-stop", "never", true, time.Minute); err != nil || d != 0 || !never {
			t.Errorf("never = %v, %v, %v", d, never, err)
		}
		if _, _, err := parseDurationFlag("--delay", "never", false, 0); err == nil {
			t.Error("never allowed when allowNever=false")
		}
		if d, never, err := parseDurationFlag("--idle-stop", "30m", true, time.Minute); err != nil || d != 30*time.Minute || never {
			t.Errorf("30m = %v, %v, %v", d, never, err)
		}
		for _, bad := range []string{"30s", "soon"} {
			_, _, err := parseDurationFlag("--idle-stop", bad, true, time.Minute)
			var ue *client.UserError
			if !errors.As(err, &ue) || ue.Kind != client.KindUsage {
				t.Errorf("parseDurationFlag(%q) = %v, want KindUsage", bad, err)
			}
		}
	})
}

func TestListFlagsAcceptRepeatedAndCommas(t *testing.T) {
	t.Run("alerts set --events", func(t *testing.T) {
		newTestHost(t)
		for _, bad := range [][]string{
			{"host", "alerts", "set", "box", "--events", ""},
			{"host", "alerts", "set", "box", "--events", ","},
			{"host", "alerts", "set", "box", "--events", " , , "},
		} {
			if got, code := lineErr(t, bad...); code != 2 || !strings.Contains(got, "--events needs at least one value") {
				t.Errorf("%v = exit %d: %s", bad, code, got)
			}
		}
		out := ok(t, "host", "alerts", "set", "box", "--command", "true", "--events", "failed", "--events", "oom,started")
		if !strings.Contains(out, "events   failed, oom, started") {
			t.Errorf("repeated --events:\n%s", out)
		}
	})

	t.Run("debug start --only", func(t *testing.T) {
		newTestHost(t)
		newSession(t, "dbglist", "/bin/sh", "-c", "exec sleep 60")
		out := okSaid(t, "debug", "start", "box/dbglist", "--only", "attach", "--only", "memory")
		if !strings.Contains(out, "(attach, memory)") {
			t.Errorf("repeated --only:\n%s", out)
		}
	})

	t.Run("host add --label", func(t *testing.T) {
		labels := parseLabels([]string{"provider=gcp,region=eu", "tier=dev"})
		if labels["provider"] != "gcp" || labels["region"] != "eu" || labels["tier"] != "dev" {
			t.Errorf("parseLabels = %+v", labels)
		}
	})

	t.Run("new --secret commas and repeated", func(t *testing.T) {
		th := newTestHost(t)
		writeSettings(t, "[secrets]\nONE = \"echo v1\"\nTWO = \"echo v2\"\nTHREE = \"echo v3\"\n")
		outFile := filepath.Join(th.h.Home, "secrets.out")
		okSaid(t, "new", "box", "--detach", "--wall", "none", "--downgrade", "--secret", "ONE,TWO", "--secret", "THREE", "--", "/bin/sh", "-c", "printf '%s,%s,%s' \"$ONE\" \"$TWO\" \"$THREE\" > "+outFile+"; exec sleep 60")
		hosttest.WaitFor(t, 10*time.Second, "secrets file", func() bool {
			b, err := os.ReadFile(outFile)
			return err == nil && string(b) == "v1,v2,v3"
		})
		if _, code := lineErr(t, "new", "box", "--detach", "--secret", ""); code != 2 {
			t.Errorf("empty --secret exited %d, want 2", code)
		}
	})

	t.Run("new --expose commas and repeated", func(t *testing.T) {
		root := Root()
		cmd, _, err := root.Find([]string{"new"})
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.ParseFlags([]string{"--expose", "ONE,TWO", "--expose", "THREE"}); err != nil {
			t.Fatal(err)
		}
		got, err := cmd.Flags().GetStringSlice("expose")
		if err != nil || !slices.Equal(got, []string{"ONE", "TWO", "THREE"}) {
			t.Errorf("--expose = %v, %v", got, err)
		}
		if _, code := lineErr(t, "new", "box", "--detach", "--expose", ""); code != 2 {
			t.Errorf("empty --expose exited %d, want 2", code)
		}
	})

	t.Run("setup --agents repeated and -n", func(t *testing.T) {
		root := Root()
		cmd, rest, err := root.Find([]string{"setup", "--host", "box", "-n", "mybox", "--agents", "claude", "--agents", "codex", "--yes"})
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.ParseFlags(rest); err != nil {
			t.Fatal(err)
		}
		if got, _ := cmd.Flags().GetString("name"); got != "mybox" {
			t.Errorf("setup -n = %q, want mybox", got)
		}
		if got, _ := cmd.Flags().GetStringSlice("agents"); !slices.Equal(got, []string{"claude", "codex"}) {
			t.Errorf("setup --agents = %v, want [claude codex]", got)
		}
	})
}

// Each C6 old form works, does what it did, warns once (plain and JSON where
// supported), is hidden from help, and stays quiet during shell completion
// (docs/contributing/cli-spec.md 3.3 and 10.5).
func TestC6OldFormsWorkAndWarnOnce(t *testing.T) {
	t.Run("root -v", func(t *testing.T) {
		hosttest.New(t)
		t.Setenv("NO_COLOR", "1")
		wantOut, wantErr, wantCode := mainRun(t, "--version")
		if wantCode != 0 || wantErr != "" || !strings.HasPrefix(wantOut, brand.Name+" version ") {
			t.Fatalf("--version: exit %d, out %q, err %q", wantCode, wantOut, wantErr)
		}
		out, errOut, code := mainRun(t, "-v")
		if code != 0 || out != wantOut || strings.Count(errOut, "Warning: ") != 1 || !strings.Contains(errOut, `"-v" is now "--version"`) {
			t.Fatalf("-v: exit %d, out %q, err %q", code, out, errOut)
		}
		if help := ok(t, "--help"); strings.Contains(help, "-v,") || !strings.Contains(help, "--version") {
			t.Errorf("root help shows -v or hides --version:\n%s", help)
		}
	})

	t.Run("new -d and -C", func(t *testing.T) {
		th := newTestHost(t)
		t.Setenv("NO_COLOR", "1")
		dirA := filepath.Join(th.h.Home, "dir-a")
		dirB := filepath.Join(th.h.Home, "dir-b")
		_ = os.MkdirAll(dirA, 0o755)
		_ = os.MkdirAll(dirB, 0o755)

		// -C sets dir with no warning.
		out, errOut, code := mainRun(t, "new", "box", "-C", dirA, "--detach", "--wall", "none", "--downgrade", "--json", "--", "sleep", "60")
		var s client.Session
		if code != 0 || errOut != "" || json.Unmarshal([]byte(out), &s) != nil || s.Cwd != dirA {
			t.Fatalf("new -C: exit %d, out %q, err %q", code, out, errOut)
		}

		// -d sets dir and warns once (plain).
		_, errOut, code = mainRun(t, "new", "box", "-d", dirB, "--detach", "--wall", "none", "--downgrade", "--", "sleep", "60")
		if code != 0 || strings.Count(errOut, "Warning: ") != 1 || !strings.Contains(errOut, `"new -d" is now "new -C"`) {
			t.Fatalf("new -d: exit %d, err %q", code, errOut)
		}
		list, _ := host.List()
		foundB := false
		for _, item := range list {
			if item.Cwd == dirB {
				foundB = true
			}
		}
		if !foundB {
			t.Errorf("new -d did not use %s: %+v", dirB, list)
		}

		// -d with --json warns as one JSON line on stderr.
		out, errOut, code = mainRun(t, "new", "box", "-d", dirA, "--detach", "--wall", "none", "--downgrade", "--json", "--", "sleep", "60")
		var warn struct {
			V       int
			Warning map[string]string
		}
		if code != 0 || json.Unmarshal([]byte(out), &s) != nil || s.Cwd != dirA ||
			json.Unmarshal([]byte(errOut), &warn) != nil || warn.Warning["old"] != "new -d" {
			t.Fatalf("new -d --json: exit %d, out %q, err %q", code, out, errOut)
		}

		// Last flag wins when -d and -C both appear, and -d still warns.
		out, errOut, code = mainRun(t, "new", "box", "-d", dirA, "-C", dirB, "--detach", "--wall", "none", "--downgrade", "--json", "--", "sleep", "60")
		if code != 0 || json.Unmarshal([]byte(out), &s) != nil || s.Cwd != dirB || !strings.Contains(errOut, `"new -d"`) {
			t.Fatalf("new -d -C: exit %d, out %q, err %q", code, out, errOut)
		}

		// Missing value on -d names -d, not the internal flag name.
		if got, code := lineErr(t, "new", "box", "-d"); code != 2 || !strings.Contains(got, "-d needs a value") {
			t.Errorf("new -d without value = exit %d:\n%s", code, got)
		}

		if help := ok(t, "new", "--help"); strings.Contains(help, "-d,") || !strings.Contains(help, "-C, --dir") {
			t.Errorf("new help:\n%s", help)
		}
	})

	t.Run("new --setup and --no-setup", func(t *testing.T) {
		th := newTestHost(t)
		t.Setenv("NO_COLOR", "1")
		repo := filepath.Join(th.h.Home, "repo")
		hook := filepath.Join(repo, "."+brand.Name, "setup")
		_ = os.MkdirAll(filepath.Dir(hook), 0o755)
		marker := filepath.Join(th.h.Home, "ran-old-setup")
		_ = os.WriteFile(hook, []byte("#!/bin/sh\necho RAN > "+marker+"\n"), 0o755)

		// --no-setup skips the script and warns once.
		_, errOut, code := mainRun(t, "new", "box", "-C", repo, "--detach", "--wall", "none", "--downgrade", "--no-setup", "--", "sleep", "60")
		if code != 0 || strings.Count(errOut, "Warning: ") != 1 || !strings.Contains(errOut, `"--no-setup" is now "--no-setup-script"`) {
			t.Fatalf("new --no-setup: exit %d, err %q", code, errOut)
		}
		if _, err := os.Stat(marker); err == nil {
			t.Error("--no-setup ran the setup script")
		}

		// --setup runs the script and warns once.
		_, errOut, code = mainRun(t, "new", "box", "-C", repo, "--detach", "--wall", "none", "--downgrade", "--setup", "--", "sleep", "60")
		if code != 0 || strings.Count(errOut, "Warning: ") != 1 || !strings.Contains(errOut, `"--setup" is now "--setup-script"`) {
			t.Fatalf("new --setup: exit %d, err %q", code, errOut)
		}
		hosttest.WaitFor(t, 10*time.Second, "setup marker", func() bool {
			_, err := os.Stat(marker)
			return err == nil
		})

		// --setup-script and --no-setup-script conflict cleanly.
		if got, code := lineErr(t, "new", "box", "--detach", "--setup-script", "--no-setup-script"); code != 2 || !strings.Contains(got, "--setup-script and --no-setup-script cannot go together") {
			t.Errorf("conflict = exit %d:\n%s", code, got)
		}
		if help := ok(t, "new", "--help"); strings.Contains(help, "--setup ") || !strings.Contains(help, "--setup-script") || !strings.Contains(help, "--no-setup-script") {
			t.Errorf("new help:\n%s", help)
		}
	})

	t.Run("host alerts set --token-cmd and host alerts log -n", func(t *testing.T) {
		newTestHost(t)
		t.Setenv("NO_COLOR", "1")
		out, errOut, code := mainRun(t, "host", "alerts", "set", "box", "--ntfy", "https://ntfy.example.com/t", "--token-cmd", "echo tok")
		if code != 0 || !strings.Contains(out, "(token set)") || strings.Count(errOut, "Warning: ") != 1 || !strings.Contains(errOut, `"--token-cmd" is now "--token-command"`) {
			t.Fatalf("alerts set --token-cmd: exit %d, out %q, err %q", code, out, errOut)
		}
		if help := ok(t, "host", "alerts", "set", "--help"); strings.Contains(help, "--token-cmd") || !strings.Contains(help, "--token-command") {
			t.Errorf("alerts set help:\n%s", help)
		}

		_, errOut, code = mainRun(t, "host", "alerts", "log", "box", "-n", "3")
		if code != 0 || strings.Count(errOut, "Warning: ") != 1 || !strings.Contains(errOut, `"host alerts log -n" is now "host alerts log --lines"`) {
			t.Fatalf("alerts log -n: exit %d, err %q", code, errOut)
		}
		out, errOut, code = mainRun(t, "host", "alerts", "log", "box", "--json", "-n", "3")
		var warn struct {
			V       int
			Warning map[string]string
		}
		if code != 0 || !json.Valid([]byte(out)) || json.Unmarshal([]byte(errOut), &warn) != nil || warn.Warning["old"] != "host alerts log -n" {
			t.Fatalf("alerts log -n --json: exit %d, out %q, err %q", code, out, errOut)
		}
		if got, code := lineErr(t, "host", "alerts", "log", "box", "-n", "abc"); code != 2 || !strings.Contains(got, `-n "abc" is not a whole number`) {
			t.Errorf("alerts log -n abc = exit %d:\n%s", code, got)
		}
		if help := ok(t, "host", "alerts", "log", "--help"); strings.Contains(help, "-n,") || !strings.Contains(help, "--lines") {
			t.Errorf("alerts log help:\n%s", help)
		}
	})

	t.Run("host create and host add --local bare --memory and --disk", func(t *testing.T) {
		m, _ := withFake(t, nil)
		t.Setenv("NO_COLOR", "1")
		cp := &captureLocalProvider{m: m}
		origCheck, origSizing, origPin, origProv := checkLocalMac, localMacSizing, ensurePinnedLima, localMacProvider
		t.Cleanup(func() {
			checkLocalMac, localMacSizing, ensurePinnedLima, localMacProvider = origCheck, origSizing, origPin, origProv
		})
		checkLocalMac = func(context.Context) error { return nil }
		localMacSizing = func(context.Context) (int, int) { return 4, 6 }
		ensurePinnedLima = func(context.Context, io.Writer) (string, error) { return "/fake/limactl", nil }
		localMacProvider = func() provider.Provider { return cp }

		// Bare numbers warn once each and mean GiB.
		_, errOut, code := mainRun(t, "host", "add", "bare-mac", "--local", "--memory", "010", "--disk", "0x30", "--yes")
		if code != 0 || cp.got.Memory != 8<<30 || cp.got.Disk != 48<<30 ||
			strings.Count(errOut, `Warning: "--memory" without a unit`) != 1 ||
			strings.Count(errOut, `Warning: "--disk" without a unit`) != 1 {
			t.Fatalf("host add --local bare numbers: exit %d, req %+v, err %q", code, cp.got, errOut)
		}

		// Sizes with units do not warn.
		_, errOut, code = mainRun(t, "host", "add", "sized-mac", "--local", "--memory", "1.5G", "--disk", "50G", "--yes")
		if code != 0 || cp.got.Memory != 1536<<20 || cp.got.Disk != 50<<30 || strings.Contains(errOut, "Warning:") {
			t.Fatalf("host add --local sized: exit %d, req %+v, err %q", code, cp.got, errOut)
		}

		// host create uses the same parser: capture with fake provider.
		var gotReq provider.Request
		origProviders := providers
		providers = append(slices.Clone(providers), &funcProvider{name: "fakebox", m: m, onCreate: func(req provider.Request) { gotReq = req }})
		t.Cleanup(func() { providers = origProviders })

		_, errOut, code = mainRun(t, "host", "create", "fakebox", "fc1", "--memory", "8", "--disk", "40")
		if code != 0 || gotReq.Memory != 8<<30 || gotReq.Disk != 40<<30 ||
			strings.Count(errOut, `Warning: "--memory" without a unit`) != 1 ||
			strings.Count(errOut, `Warning: "--disk" without a unit`) != 1 {
			t.Fatalf("host create bare: exit %d, req %+v, err %q", code, gotReq, errOut)
		}
		_, errOut, code = mainRun(t, "host", "create", "fakebox", "fc2", "--memory", "8G", "--disk", "40G")
		if code != 0 || gotReq.Memory != 8<<30 || gotReq.Disk != 40<<30 || strings.Contains(errOut, "Warning:") {
			t.Fatalf("host create sized: exit %d, req %+v, err %q", code, gotReq, errOut)
		}
		if _, code := lineErr(t, "host", "create", "fakebox", "fc3", "--memory", "1.5"); code != 2 {
			t.Errorf("host create --memory 1.5 exited %d, want 2", code)
		}
	})

	t.Run("completion stays quiet for all old flags", func(t *testing.T) {
		hosttest.New(t)
		quiet := func(errOut string) bool {
			for _, l := range strings.Split(strings.TrimSpace(errOut), "\n") {
				if l != "" && !strings.HasPrefix(l, "Completion ended with directive:") {
					return false
				}
			}
			return true
		}
		for _, args := range [][]string{
			{cobra.ShellCompRequestCmd, "-v", ""},
			{cobra.ShellCompRequestCmd, "new", "box", "-d", ""},
			{cobra.ShellCompRequestCmd, "new", "box", "--setup", ""},
			{cobra.ShellCompRequestCmd, "new", "box", "--no-setup", ""},
			{cobra.ShellCompRequestCmd, "host", "alerts", "set", "box", "--token-cmd", ""},
			{cobra.ShellCompRequestCmd, "host", "alerts", "log", "box", "-n", ""},
			{cobra.ShellCompRequestCmd, "agents", "install", "claude", "--force", ""},
		} {
			_, errOut, err := runApart(t, args...)
			if err != nil || !quiet(errOut) {
				t.Errorf("completion %v: err %v, stderr %q", args, err, errOut)
			}
		}
	})
}

// funcProvider is a fake provider that captures provider.Request for host create tests.
type funcProvider struct {
	name     string
	m        client.Lifecycle
	onCreate func(provider.Request)
}

func (p *funcProvider) Name() string { return p.name }

func (p *funcProvider) Create(_ context.Context, req provider.Request) (client.Host, error) {
	if p.onCreate != nil {
		p.onCreate(req)
	}
	return client.Host{
		Name:    req.Name,
		Machine: &client.Machine{Provider: "fake", ID: provider.MachineName(req.Name)},
	}, nil
}

func (p *funcProvider) Open(client.Host) (client.Lifecycle, error) { return p.m, nil }

// new rejects two words before "--", and takes a missing host before "--" as a
// missing argument (docs/contributing/cli-spec.md 10.4 and isolation-spec 14.14).
func TestNewTwoWordsBeforeDash(t *testing.T) {
	newTestHost(t)
	got, code := lineErr(t, "new", "box", "htop")
	if code != 2 || !strings.Contains(got, "new takes one host. A command goes after --.") || !strings.Contains(got, "Try: "+brand.Name+" new box -- htop\n") {
		t.Errorf("new box htop = exit %d:\n%s", code, got)
	}
	got, code = lineErr(t, "new", "--", "htop")
	if code != 2 || !strings.Contains(got, "new needs <host>") {
		t.Errorf("new -- htop = exit %d:\n%s", code, got)
	}
}

// After new -d ends, -d means --detach: `new box -d ~/src` fails with exit
// code 2 (two words before --), and `new -d ~/src` fails with exit code 3 (no
// host named "~/src") (docs/contributing/cli-spec.md 10.4 and 10.5).
func TestNewShortDAfterTheEnd(t *testing.T) {
	newTestHost(t)
	old := oldNewShortDir
	oldNewShortDir = nil
	t.Cleanup(func() { oldNewShortDir = old })

	// With a host: ~/src becomes a second word before "--" -> exit code 2.
	got, code := lineErr(t, "new", "box", "-d", "~/src")
	if code != 2 || !strings.Contains(got, "new takes one host. A command goes after --.") {
		t.Errorf("new box -d ~/src after end = exit %d:\n%s", code, got)
	}

	// Without a host: ~/src is read as the host name -> exit code 3.
	got, code = lineErr(t, "new", "-d", "~/src")
	if code != 3 || !strings.Contains(got, `no host named "~/src"`) {
		t.Errorf("new -d ~/src after end = exit %d:\n%s", code, got)
	}

	// -d actually detaches without warning.
	out, errOut, code := mainRun(t, "new", "box", "-d", "--wall", "none", "--downgrade", "--json", "--", "sleep", "60")
	var s client.Session
	if code != 0 || errOut != "" || json.Unmarshal([]byte(out), &s) != nil || s.ID == "" {
		t.Fatalf("new box -d --json after end: exit %d, out %q, err %q", code, out, errOut)
	}
}
