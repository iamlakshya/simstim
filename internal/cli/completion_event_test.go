package cli

import (
	"slices"
	"testing"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/alerts"
)

func TestAlertEventCompletionUsesTheActualKindTable(t *testing.T) {
	isolateCompletionEnv(t)
	got, directive, stderr := execComplete(t, nil, cobra.ShellCompRequestCmd, "host", "alerts", "set", "missing", "--events", "")
	if !slices.Equal(got, alerts.Kinds) || directive != cobra.ShellCompDirectiveNoFileComp|cobra.ShellCompDirectiveNoSpace || stderr != "" {
		t.Fatalf("event completion diverged from admission: %q, %d, %q, want %q", got, directive, stderr, alerts.Kinds)
	}
}
