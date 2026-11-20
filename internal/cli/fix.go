package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

// offerFixes shows each fix, with what it does and the command it runs, and
// asks before each one unless yes. It applies the chosen ones with apply and
// prints how each went. A manual fix is printed for the user to run.
func offerFixes(in io.Reader, w io.Writer, fixes []api.Fix, yes bool, apply func(ids []string) ([]api.Fix, error)) error {
	if len(fixes) == 0 {
		fmt.Fprintln(w, "\nNothing to fix.")
		return nil
	}
	fmt.Fprintf(w, "\nFound %d to fix:\n", len(fixes))
	answers := bufio.NewReader(in)
	var chosen []string
	for i, f := range fixes {
		f = cleanFix(f)
		fmt.Fprintf(w, "\n%d. %s.\n", i+1, sentenceCase(f.Problem))
		if f.Manual {
			fmt.Fprintf(w, "   This one is yours to do: %s.\n   Run: %s\n", f.Action, f.Command)
			continue
		}
		fmt.Fprintf(w, "   Fix: %s.\n   Runs: %s\n", f.Action, f.Command)
		if yes || ask(answers, w, "   Fix it?") {
			chosen = append(chosen, f.ID)
		}
	}
	if len(chosen) == 0 {
		fmt.Fprintln(w, "\nNothing was changed.")
		return nil
	}
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
			fmt.Fprintf(w, "✓ fixed: %s\n", f.Problem)
		case f.Error != "":
			failed++
			fmt.Fprintf(w, "✕ not fixed: %s\n    %s\n", f.Problem, f.Error)
		}
	}
	if failed > 0 {
		return &client.UserError{Msg: fmt.Sprintf("%d of %d fixes failed", failed, len(chosen)), Hint: "Read each one's reason above; a fix that needs root says which command to run."}
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

// ask asks a yes-or-no question; anything but yes is no.
func ask(r *bufio.Reader, w io.Writer, q string) bool {
	fmt.Fprintf(w, "%s [y/N] ", q)
	line, _ := r.ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes"
}

func sentenceCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// printChecks prints a doctor's checks, with what to try for each failure.
func printChecks(w io.Writer, checks []api.Check) {
	for _, c := range checks {
		mark := "✓"
		if !c.OK {
			mark = "✕"
		}
		fmt.Fprintf(w, "  %s %-22s %s\n", mark, c.Name, c.Detail)
		if !c.OK && c.Fix != "" {
			fmt.Fprintf(w, "    Try: %s\n", c.Fix)
		}
	}
}

// fixHost offers the host doctor's fixes.
func fixHost(ctx context.Context, conn *client.Conn, h client.Host, yes bool) error {
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	fixes, err := conn.HostFixes(cctx, false, nil)
	cancel()
	if err != nil {
		return err
	}
	for i := range fixes {
		fixes[i].Command = strings.ReplaceAll(fixes[i].Command, "<host>", h.SSH)
	}
	return offerFixes(os.Stdin, os.Stdout, fixes, yes, func(ids []string) ([]api.Fix, error) {
		actx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		return conn.HostFixes(actx, true, ids)
	})
}
