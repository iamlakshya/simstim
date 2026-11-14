package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestCompletionFlagErrorsStayQuiet(t *testing.T) {
	isolateCompletionEnv(t)
	for _, name := range []string{cobra.ShellCompRequestCmd, cobra.ShellCompNoDescRequestCmd} {
		for _, args := range [][]string{{"attach", "--unknown-flag", ""}, {"--json", "attach", ""}} {
			items, directive, stderr := execComplete(t, nil, name, args...)
			if len(items) != 0 || directive != cobra.ShellCompDirectiveNoFileComp || stderr != "" {
				t.Errorf("%s %q = %q directive=%d stderr=%q", name, args, items, directive, stderr)
			}
		}
		// File-valued flags deliberately retain local-file completion.
		_, directive, stderr := execComplete(t, nil, name, "host", "add", "box", "--ssh-config", "")
		if directive != cobra.ShellCompDirectiveDefault || stderr != "" {
			t.Errorf("file completion changed: directive=%d stderr=%q", directive, stderr)
		}
	}
}

func TestCompletionErrorsMainAndDebugFile(t *testing.T) {
	isolateCompletionEnv(t)
	debug := filepath.Join(t.TempDir(), "completion.debug")
	for _, name := range []string{cobra.ShellCompRequestCmd, cobra.ShellCompNoDescRequestCmd} {
		for _, args := range [][]string{nil, {"attach", "--unknown-flag", ""}, {"--json", "attach", ""}} {
			cmd := exec.Command(os.Args[0], "-test.run=^TestMainInAChild$")
			full := append([]string{name}, args...)
			cmd.Env = append(os.Environ(), mainArgsEnv+"="+strings.Join(full, mainArgsSep), mainSceneEnv+"=", "BASH_COMP_DEBUG_FILE="+debug)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Run(); err != nil || stdout.String() != ":4\n" || stderr.Len() != 0 {
				t.Errorf("Main %q: %v stdout=%q stderr=%q", full, err, stdout.String(), stderr.String())
			}
		}
	}
	b, err := os.ReadFile(debug)
	if err != nil || !strings.Contains(string(b), "Error while parsing flags") {
		t.Errorf("completion error did not reach its explicit debug file: %q, %v", b, err)
	}
}

func TestCompletionRestoresProcessStderr(t *testing.T) {
	isolateCompletionEnv(t)
	before := os.Stderr
	r := Root()
	r.SetOut(&bytes.Buffer{})
	r.SetArgs([]string{cobra.ShellCompRequestCmd, "attach", "--unknown-flag", ""})
	if err := r.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if os.Stderr != before {
		t.Fatal("completion did not restore the process stderr")
	}
}

func TestNewCompletionUsesTheRealDash(t *testing.T) {
	cfg, _ := isolateCompletionEnv(t)
	if err := os.WriteFile(filepath.Join(cfg, "hosts.toml"), []byte("[[host]]\nname = 'box'\nssh = 'box'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args []string
		want bool
	}{
		{[]string{"new", ""}, true},
		{[]string{"new", "--dir", "--", ""}, true},
		{[]string{"new", "--dir=--", ""}, true},
		{[]string{"new", "--", ""}, false},
		{[]string{"new", "box", "--", ""}, false},
	} {
		items, directive, stderr := execComplete(t, nil, cobra.ShellCompRequestCmd, tc.args...)
		if (len(items) != 0) != tc.want || directive != cobra.ShellCompDirectiveNoFileComp || stderr != "" {
			t.Errorf("%q = %q, %d, %q, want host=%v", tc.args, items, directive, stderr, tc.want)
		}
	}
}
