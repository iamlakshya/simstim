package cli

import (
	"encoding/json"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/diag"
)

// Each old form ends two minor releases after the release that adds its
// warning, and 12 months after that release's day
// (docs/contributing/cli-spec.md 3.3).
func TestOldFormsEnd(t *testing.T) {
	for _, o := range oldForms {
		version, day := o.End()
		major, minor, _ := strings.Cut(o.Version, ".")
		m, _ := strconv.Atoi(strings.Split(minor, ".")[0])
		if want := major + "." + strconv.Itoa(m+2) + ".0"; version != want {
			t.Errorf("%s: ends at %s, want %s", o.Old, version, want)
		}
		released, err := time.Parse(time.DateOnly, releaseDays[o.Version])
		if err != nil || !day.Equal(released.AddDate(1, 0, 0)) {
			t.Errorf("%s: ends on %s, want 12 months after the day of %s (%q)", o.Old, day, o.Version, releaseDays[o.Version])
		}
		if o.Code == "" || o.New == "" || o.Kind == "" {
			t.Errorf("%s: a row without a code, a new form or a kind", o.Old)
		}
		// A row of the root, such as -v, has no command path.
		if c, rest, err := Root().Find(strings.Fields(o.Cmd)); err != nil || len(rest) != 0 || shortPath(c) != o.Cmd {
			t.Errorf("%s: %q is not a command", o.Old, o.Cmd)
		}
	}
	for v, want := range map[string]string{"0.2.0": "0.4.0", "v1.9.3": "1.11.0"} {
		if got := twoReleasesAfter(v); got != want {
			t.Errorf("twoReleasesAfter(%s) = %s, want %s", v, got, want)
		}
	}
	releaseDays["9.9.0"] = "2030-01-31"
	defer delete(releaseDays, "9.9.0")
	o := oldForm{Old: "kill", New: "rm", Version: "9.9.0"}
	if got := o.Warning(); got != `"kill" is now "rm". "kill" works until v9.11.0 and 31 January 2031, whichever comes later.` {
		t.Errorf("warning = %q", got)
	}
	if olds := OldNames("host ls"); len(olds) != 1 || !strings.HasPrefix(olds[0].Until, "v0.4.0 and ") {
		t.Errorf("OldNames(host ls) = %+v", olds)
	}
}

// warnOld prints once per run, as one line or one JSON line, and never
// during shell completion.
func TestWarnOld(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	root := Root()
	cmd, _, _ := root.Find([]string{"host", "ls"})
	_, errOut := captureOutput(t, func() {
		warnOld(cmd, oldHostKeys)
		warnOld(cmd, oldHostKeys)
	})
	_, day := oldHostKeys.End()
	if strings.Count(errOut, "Warning: ") != 1 || !strings.Contains(errOut, "The old keys print until v0.4.0 and "+day.Format("2 January 2006")+", whichever comes later.") {
		t.Errorf("warned %q", errOut)
	}

	root = Root()
	cmd, _, _ = root.Find([]string{"host", "ls"})
	_ = cmd.ParseFlags([]string{"--json"})
	_, errOut = captureOutput(t, func() { warnOld(cmd, oldHostKeys) })
	var line struct {
		V       int
		Warning map[string]string
	}
	if err := json.Unmarshal([]byte(errOut), &line); err != nil || line.V != 1 || line.Warning["until_version"] != "0.4.0" ||
		line.Warning["until_date"] != day.Format(time.DateOnly) || line.Warning["code"] != "old_keys" || strings.Count(errOut, "\n") != 1 {
		t.Errorf("the JSON warning is %q", errOut)
	}

	complete := &cobra.Command{Use: cobra.ShellCompRequestCmd}
	Root().AddCommand(complete)
	if _, errOut = captureOutput(t, func() { warnOld(complete, oldHostKeys) }); errOut != "" {
		t.Errorf("a warning during completion: %q", errOut)
	}
}

// The shell's completion of a command with an old form works, and prints
// nothing of its own on stderr, with debug on too: only Cobra's line about
// the directive, which the shells drop. It fails when completion breaks.
func TestCompletionOfAnOldFormIsQuiet(t *testing.T) {
	t.Setenv(diag.EnvFile, filepath.Join(t.TempDir(), "debug.jsonl"))
	t.Cleanup(func() { client.StopDebug(io.Discard) })
	quiet := func(errOut string) bool {
		for _, l := range strings.Split(strings.TrimSpace(errOut), "\n") {
			if l != "" && !strings.HasPrefix(l, "Completion ended with directive:") {
				return false
			}
		}
		return true
	}
	out, errOut, err := runApart(t, cobra.ShellCompRequestCmd, "host", "l")
	if err != nil || !strings.Contains(out, "ls\t") || !quiet(errOut) {
		t.Errorf("completion of host l: %v\nstdout %q\nstderr %q", err, out, errOut)
	}
	out, errOut, err = runApart(t, cobra.ShellCompRequestCmd, "host", "ls", "--js")
	if err != nil || !strings.Contains(out, "--json") || !quiet(errOut) {
		t.Errorf("completion of host ls --js: %v\nstdout %q\nstderr %q", err, out, errOut)
	}
}
