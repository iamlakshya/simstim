package cli

import (
	"os"
	"testing"

	"github.com/spf13/cobra"
)

// The child still runs Main. Its completer waits for Main's real signal
// context, so the signal cannot arrive before completion begins or after
// Main has already decided its exit code.
func completionSignalScene() {
	completeSessionArg = func(cmd *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		_, _ = os.Stdout.WriteString("completion ready\n")
		<-cmd.Context().Done()
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
}
func TestCompletionMainHandlesAnEmptyRequest(t *testing.T) {
	for _, name := range []string{cobra.ShellCompRequestCmd, cobra.ShellCompNoDescRequestCmd} {
		cmd, stderr := childWithoutTerminal(t, "", name)
		if code := exitOf(t, cmd); code != 0 || stderr.Len() != 0 {
			t.Errorf("%s exit=%d stderr=%q", name, code, stderr.String())
		}
	}
}
