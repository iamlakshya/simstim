package cli

import (
	"context"
	"errors"
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/client"
)

func selectedNewArgumentV1(cmd *cobra.Command, args []string) string {
	before := len(args)
	if dash := cmd.ArgsLenAtDash(); dash >= 0 {
		before = dash
	}
	if before == 1 {
		return args[0]
	}
	return ""
}

// A UI answer continues only the same concrete attempt. The final check
// observes actual readiness even when a question runner skips its closure.
func nextSelectedNewAttemptV1(cmd *cobra.Command, plan *client.HostAttemptPlanV1) (*client.HostAttemptV1, error, bool) {
	attempt, err := plan.Next(cmd.Context(), true)
	if err == nil || attempt == nil {
		return attempt, err, true
	}
	var question *client.ConfirmError
	if !errors.As(err, &question) {
		return attempt, err, true
	}
	original := cmd.Context()
	cmd.SetContext(attempt.Context())
	defer cmd.SetContext(original)
	first, returned := true, err
	var actual error
	err = grantQuestions(cmd, func(ctx context.Context) error {
		if first {
			first = false
			return returned
		}
		attempt, actual = attempt.Continue(ctx, true)
		return actual
	})
	if err != nil {
		return attempt, err, actual != nil && err == actual
	}
	// Check the actual transition directly; Continue is reserved for its
	// original pending wake question. A skipped closure grants nothing.
	err = attempt.CheckReadyV1(cmd.Context())
	return attempt, err, err == nil
}

func dryRunNewPlacementV1(cmd *cobra.Command, selection *client.HostSelectionV1, args client.NewArgs, need client.PlacementNeed, options client.AutoPlacementOptionsV1) (returned error) {
	if !selection.Automatic() {
		return policyUsage("--dry-run needs automatic placement", "Use new auto --dry-run.", "flags_conflict")
	}
	plan, err := client.PrepareAutoPlacementV1(cmd.Context(), selection, args, need, options)
	if plan != nil {
		defer func() { returned = errors.Join(returned, plan.Close(cmd.Context())) }()
	}
	if err != nil {
		return err
	}
	rows, summary := plan.Rows(), plan.Summary()
	if jsonOn(cmd) {
		return printJSON(map[string]any{"summary": summary, "hosts": rows})
	}
	out := prompts(cmd).out
	if summary.Available {
		fmt.Fprintf(out, "Sandbox: %.1f GiB, %d CPUs, %s, %s\n", float64(summary.Memory)/(1<<30), summary.CPUs, shown(summary.Level), shown(summary.Image))
	}
	table := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(table, "HOST\tVERDICT\tWHY\tROOM\tIMAGE\tRTT\tLEVEL")
	for _, row := range rows {
		room, roundTrip := "-", "-"
		if row.RoomKnown {
			room = fmt.Sprintf("%.1fG", float64(row.Room)/(1<<30))
		}
		if row.RoundTripKnown {
			roundTrip = row.RoundTrip.Round(time.Millisecond).String()
		}
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", shown(row.Host), shown(row.Verdict), shown(row.Reason), room, shown(row.Image), roundTrip, shown(row.Level))
	}
	return table.Flush()
}
