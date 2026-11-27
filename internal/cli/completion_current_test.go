package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

func completionValues(rows []string) []string {
	values := make([]string, len(rows))
	for i, row := range rows {
		values[i], _, _ = strings.Cut(row, "\t")
	}
	return values
}

func TestLivePolicyDoorCompletionMatchesItsParser(t *testing.T) {
	isolateCompletionEnv(t)
	for _, verb := range []string{"allow", "deny"} {
		rows, directive, stderr := execComplete(t, nil, cobra.ShellCompRequestCmd, "policy", verb, "")
		want := []string{"net", "push", "ssh-key", "port"}
		if !slices.Equal(rows, want) || directive != cobra.ShellCompDirectiveNoFileComp || stderr != "" {
			t.Fatalf("policy %s doors = %q, %d, %q", verb, rows, directive, stderr)
		}
		for _, door := range rows {
			if _, _, err := policyTarget([]string{door, "example.org"}); err != nil {
				t.Fatalf("completion offered a rejected door %q: %v", door, err)
			}
		}
		rows, directive, stderr = execComplete(t, nil, cobra.ShellCompRequestCmd, "policy", verb, "net", "")
		if len(rows) != 0 || directive != cobra.ShellCompDirectiveNoFileComp || stderr != "" {
			t.Fatal("a target argument offered unsupported doors or files", rows, directive, stderr)
		}
	}
	for _, prefix := range []string{"", "n", "p"} {
		rows, directive, stderr := execComplete(t, nil, cobra.ShellCompRequestCmd, "policy", "log", "--door", prefix)
		var want []string
		if strings.HasPrefix("net", prefix) {
			want = []string{"net"}
		}
		if !slices.Equal(rows, want) || directive != cobra.ShellCompDirectiveNoFileComp || stderr != "" {
			t.Fatalf("net-only log door %q = %q, %d, %q", prefix, rows, directive, stderr)
		}
	}
}

func TestLiveSessionFlagsUseCachedAddresses(t *testing.T) {
	cfg, state := isolateCompletionEnv(t)
	writeCompletionFixtures(t, cfg, state, time.Now())
	for _, args := range [][]string{
		{"policy", "ls", "--in", "atlas/"},
		{"policy", "log", "--in", "atlas/"},
		{"policy", "allow", "example.org", "--in", "atlas/"},
		{"policy", "refuse", "--in", "atlas/"},
		{"new", "--in", "atlas/"},
	} {
		rows, directive, stderr := execComplete(t, nil, cobra.ShellCompRequestCmd, args...)
		const atlas = "atlas/"
		want := []string{atlas + "auth-fix", atlas + "efgh2345", atlas + "ijkl2345"}
		if !slices.Equal(completionValues(rows), want) || directive != cobra.ShellCompDirectiveNoFileComp || stderr != "" {
			t.Fatalf("actual session flag %q = %q, %d, %q", args, rows, directive, stderr)
		}
	}
	rows, directive, stderr := execComplete(t, nil, cobra.ShellCompRequestCmd, "new", "box", "--in", "")
	if !slices.Equal(completionValues(rows), []string{"box/web", "box/qrst2345"}) || directive != cobra.ShellCompDirectiveNoFileComp || stderr != "" {
		t.Fatal("new offered a sandbox on a different explicit host", rows, directive, stderr)
	}
}

func TestCancelledCompletionReturnsNoLocalValues(t *testing.T) {
	cfg, state := isolateCompletionEnv(t)
	writeCompletionFixtures(t, cfg, state, time.Now())
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, args := range [][]string{
		{"attach", ""}, {"host", "show", ""}, {"new", "--in", ""},
		{"new", "box", "--secret", ""}, {"new", "box", "--profile", ""}, {"policy", "rm", ""},
	} {
		rows, directive, stderr := execCompleteContext(t, ctx, Root(), cobra.ShellCompRequestCmd, args...)
		if len(rows) != 0 || directive != cobra.ShellCompDirectiveNoFileComp || stderr != "" {
			t.Fatalf("cancelled completion %q = %q, %d, %q", args, rows, directive, stderr)
		}
	}
}

func TestCancelledCompletionDoesNotReadAnOpenedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache")
	if err := os.WriteFile(path, []byte("original local bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if data, ok := readCompletionData(file, ctx); ok || data != nil {
		t.Fatal("cancelled file completion returned bytes", string(data), ok)
	}
	if offset, err := file.Seek(0, io.SeekCurrent); err != nil || offset != 0 {
		t.Fatal("cancelled completion consumed its original descriptor", offset, err)
	}
	data, ok := readCompletionData(file, t.Context())
	if !ok || string(data) != "original local bytes" {
		t.Fatal("cancellation closed or replaced the caller's original file", string(data), ok)
	}
}

func TestHelpCompletionUsesActualCommandsAndHiddenTopics(t *testing.T) {
	isolateCompletionEnv(t)
	for _, test := range []struct {
		args []string
		want []string
	}{
		{[]string{"help", "con"}, []string{"concepts", "config"}},
		{[]string{"help", "exit"}, []string{"exit-codes"}},
		{[]string{"help", "host", "pre"}, []string{"prepare"}},
		{[]string{"help", "host", "def"}, []string{"default"}},
		{[]string{"help", "host", "in"}, []string{"invite"}},
		{[]string{"help", "host", "jo"}, []string{"join"}},
		{[]string{"help", "peek", ""}, nil},
	} {
		rows, directive, stderr := execComplete(t, nil, cobra.ShellCompRequestCmd, test.args...)
		if !slices.Equal(completionValues(rows), test.want) || directive != cobra.ShellCompDirectiveNoFileComp || stderr != "" {
			t.Fatalf("actual help completion %q = %q, %d, %q", test.args, rows, directive, stderr)
		}
	}
	rows, _, _ := execComplete(t, nil, cobra.ShellCompRequestCmd, "help", "")
	for _, old := range []string{"peek", "flip", "kill"} {
		if slices.Contains(completionValues(rows), old) {
			t.Fatal("old command completed in help", old)
		}
	}
}
