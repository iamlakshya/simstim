package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

// fixStage is called at stages of offerFixes: "apply", after its last
// look for a cancel, just before it applies the chosen fixes. Tests cancel
// there, so that only apply can see the cancel.
var fixStage = func(string) {}

// offerFixes shows each fix, with what it does and the command it runs, and
// asks q before each one unless yes. It applies the chosen ones with apply
// and prints how each went, all on p's output. A manual fix is printed for
// the user to run. With input off and without yes, the first question
// fails, and names -y.
func offerFixes(p *prompter, q *question, fixes []api.Fix, yes bool, apply func(ids []string) ([]api.Fix, error)) error {
	w := p.out
	if len(fixes) == 0 {
		fmt.Fprintln(w, "\nNothing to fix.")
		return nil
	}
	fmt.Fprintf(w, "\nFound %d to fix:\n", len(fixes))
	var chosen []string
	for i, f := range fixes {
		f = cleanFix(f)
		fmt.Fprintf(w, "\n%d. %s.\n", i+1, sentenceCase(f.Problem))
		if f.Manual {
			fmt.Fprintf(w, "   This one is yours to do: %s.\n   Run: %s\n", shown(f.Action), shown(f.Command))
			continue
		}
		fmt.Fprintf(w, "   Fix: %s.\n   Runs: %s\n", shown(f.Action), shown(f.Command))
		ok := yes
		if !ok {
			var err error
			if ok, err = p.confirm(q, "   Fix it?", "a yes for each fix"); err != nil {
				return err
			}
		}
		if ok {
			chosen = append(chosen, f.ID)
		}
	}
	if len(chosen) == 0 {
		fmt.Fprintln(w, "\nNothing was changed.")
		return nil
	}
	if stopped(p.ctx) {
		// A signal that came with the last answer, or before the first
		// fix when nothing was asked: fix nothing.
		if yes {
			return cancelled()
		}
		return errInterrupted
	}
	fixStage("apply")
	res, err := apply(chosen)
	if err != nil {
		return err
	}
	fmt.Fprintln(w)
	failed := 0
	for _, f := range res {
		f = cleanFix(f)
		switch {
		case f.Done:
			fmt.Fprintf(w, "✓ fixed: %s\n", shown(f.Problem))
		case f.Error != "":
			failed++
			fmt.Fprintf(w, "✕ not fixed: %s\n    %s\n", shown(f.Problem), shown(f.Error))
		}
	}
	if failed > 0 {
		return &client.UserError{Msg: fmt.Sprintf("%d of %d fixes failed", failed, len(chosen)), Hint: "Read the reason of each fix above: a fix that needs root names the command to run.", Code: "fix_failed"}
	}
	return nil
}

// cleanFix cleans the text of a fix from a host, to print. Its ID stays as
// the host sent it, for the request that applies the fix.
func cleanFix(f api.Fix) api.Fix {
	f.Problem, f.Action, f.Error = client.CleanLine(f.Problem), client.CleanLine(f.Action), client.CleanLine(f.Error)
	f.Command = client.CleanPath(f.Command) // the longer limit of a path, for a long command
	return f
}

// applyLocal applies the local fixes of ids, until ctx ends or a signal
// comes: then it applies nothing more. It returns the fixes as they ended,
// and after a cancel, the cancel that lists what was done before it
// (fixesCancelled). The client looks for the cancel before each fix and
// each file (client.ApplyLocalFixes): one place, for every caller.
func applyLocal(ctx context.Context, ids []string) ([]api.Fix, error) {
	sctx, cancel := stopping(ctx)
	defer cancel()
	res, err := client.ApplyLocalFixes(sctx, ids)
	if err != nil {
		return res, fixesCancelled(res, err)
	}
	return res, nil
}

// fixesCancelled is the cancel of fixes that a cancel stopped, with the
// fixes as they ended and the client's error. As every cancel does
// (cli-spec 6.2, rule 6), it says what was done before it: each fix that
// ran, and a fix that the cancel stopped part way, by how far it got
// (client.StoppedFixesError). Its data holds every fix as it ended, with each
// change it made, for --json.
func fixesCancelled(res []api.Fix, err error) error {
	var stopped *client.StoppedFixesError
	var done []string
	if errors.As(err, &stopped) {
		done = stopped.Done
	}
	ce := &cancelError{msg: cancelLine("Cancelled during the fixes.", done)}
	if res == nil {
		res = []api.Fix{}
	}
	ce.data, _ = json.Marshal(map[string]any{"fixes": res})
	return ce
}

// applyAll applies every fix that is not the user's own to do, without a
// question, as --fix -y --json does, and returns the fixes as they ended.
// When a fix fails, the error holds them all in its data.
func applyAll(fixes []api.Fix, apply func(ids []string) ([]api.Fix, error)) ([]api.Fix, error) {
	var ids []string
	for _, f := range fixes {
		if !f.Manual {
			ids = append(ids, f.ID)
		}
	}
	if len(ids) == 0 {
		return append([]api.Fix{}, fixes...), nil
	}
	res, err := apply(ids)
	if err != nil {
		return nil, err
	}
	failed := 0
	for _, f := range res {
		if f.Error != "" {
			failed++
		}
	}
	if failed > 0 {
		data, _ := json.Marshal(map[string]any{"fixes": res})
		return nil, &client.UserError{Msg: fmt.Sprintf("%d of %d fixes failed", failed, len(ids)),
			Hint: "Read the error of each fix in the data: a fix that needs root names the command to run.", Code: "fix_failed", Data: data}
	}
	return append([]api.Fix{}, res...), nil
}

func sentenceCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// printChecks prints a doctor's checks on rows, and what to try for each
// failure on hints: the rows are the data, on stdout, and the hints are
// messages, on stderr.
func printChecks(rows, hints io.Writer, checks []api.Check) {
	for _, c := range client.CleanChecks(checks) {
		mark := "✓"
		if !c.OK {
			mark = "✕"
		}
		fmt.Fprintf(rows, "  %s %-22s %s\n", mark, shown(c.Name), shown(c.Detail))
		if !c.OK && c.Fix != "" {
			fmt.Fprintf(hints, "    Try: %s\n", shown(c.Fix))
		}
	}
}

// fixHost offers the host doctor's fixes.
func fixHost(cmd *cobra.Command, conn *client.Conn, h client.Host, yes bool) error {
	ctx := cmd.Context()
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	fixes, err := conn.HostFixes(cctx, false, nil)
	cancel()
	if err != nil {
		return err
	}
	for i := range fixes {
		fixes[i].Command = strings.ReplaceAll(fixes[i].Command, "<host>", h.SSH)
	}
	return offerFixes(prompts(cmd), qHostFix, fixes, yes, func(ids []string) ([]api.Fix, error) {
		actx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		return conn.HostFixes(actx, true, ids)
	})
}
