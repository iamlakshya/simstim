package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

type pruneHostJSON struct {
	Host          string             `json:"host"`
	DryRun        bool               `json:"dry_run,omitempty"`
	Sessions      []api.PruneSession `json:"sessions"`
	RemovedImages []string           `json:"removed_images,omitempty"`
	Error         string             `json:"error,omitempty"`
}

func pruneCmd() *cobra.Command {
	var stoppedBefore string
	var dryRun, yes, asJSON bool
	cmd := &cobra.Command{
		Use:   "prune [<host>]",
		Short: "Remove stopped sessions with no unpulled commits",
		Long: "Lists stopped sessions that have no commits waiting to be pulled, and removes them after a\n" +
			"question. Sessions with unpulled commits are kept and listed with their count. With --dry-run,\n" +
			"nothing is removed.",
		Example: "  " + brand.Name + " prune\n" +
			"  " + brand.Name + " prune my-vps --stopped-before 7d --dry-run\n" +
			"  " + brand.Name + " prune my-vps -y --json",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPrune(cmd, args, stoppedBefore, dryRun, yes, asJSON)
		},
	}
	cmd.Flags().StringVar(&stoppedBefore, "stopped-before", "", "only sessions stopped before this age or time (such as 7d, 24h or 2026-01-01T00:00:00Z)")
	addDryRun(cmd, &dryRun, "list what prune would remove without removing anything")
	addYes(cmd, &yes, "remove stopped sessions without asking")
	addJSON(cmd, &asJSON)
	return cmd
}

func parseStoppedBefore(raw string, now time.Time) (*time.Time, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		u := t.UTC()
		return &u, nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		u := t.UTC()
		return &u, nil
	}
	if d, err := api.ParseRunDuration(s); err == nil && d > 0 {
		u := now.UTC().Add(-d)
		return &u, nil
	}
	return nil, &client.UserError{
		Msg:  fmt.Sprintf("--stopped-before: %q is not a duration or RFC 3339 time", raw),
		Hint: "Pass a duration such as 7d or 24h, or a time such as 2026-01-01T00:00:00Z.",
		Code: "bad_value",
		Kind: client.KindUsage,
	}
}

func runPrune(cmd *cobra.Command, args []string, stoppedBefore string, dryRun, yes, asJSON bool) error {
	cutoff, err := parseStoppedBefore(stoppedBefore, time.Now())
	if err != nil {
		return err
	}
	hosts, singleHost, err := resolvePruneHosts(args)
	if err != nil {
		return err
	}
	preview, err := queryPruneHosts(cmd.Context(), hosts, singleHost, api.PruneArgs{
		StoppedBefore: cutoff,
		DryRun:        true,
	})
	if err != nil {
		return err
	}
	removable := countRemovableSessions(preview)
	if dryRun {
		return outputPruneResult(preview, true, asJSON)
	}
	if removable == 0 {
		return outputNothingToPrune(cmd, preview, asJSON)
	}
	if !yes {
		if err := confirmPrune(cmd, preview, removable, asJSON); err != nil {
			return err
		}
	}
	results, err := queryPruneHosts(cmd.Context(), hosts, singleHost, api.PruneArgs{
		StoppedBefore: cutoff,
		DryRun:        false,
	})
	if err != nil {
		return err
	}
	forgetRemovedSessions(results)
	return outputPruneResult(results, false, asJSON)
}

func countRemovableSessions(preview []pruneHostJSON) int {
	n := 0
	for _, r := range preview {
		for _, s := range r.Sessions {
			if s.Action == "would_remove" {
				n++
			}
		}
	}
	return n
}

func outputPruneResult(results []pruneHostJSON, dryRun, asJSON bool) error {
	if asJSON {
		return printJSON(results)
	}
	printPruneTable(os.Stdout, results, dryRun)
	return nil
}

func outputNothingToPrune(cmd *cobra.Command, preview []pruneHostJSON, asJSON bool) error {
	if asJSON {
		for i := range preview {
			preview[i].DryRun = false
		}
		return printJSON(preview)
	}
	printPruneTable(os.Stdout, preview, false)
	say(cmd, "No stopped sessions to remove.\n")
	return nil
}

func confirmPrune(cmd *cobra.Command, preview []pruneHostJSON, removable int, asJSON bool) error {
	if !asJSON {
		printPruneTable(os.Stderr, preview, true)
	}
	noun := "sessions"
	if removable == 1 {
		noun = "session"
	}
	ok, err := prompts(cmd).confirm(
		qPrune,
		fmt.Sprintf("Remove %d stopped %s?", removable, noun),
		fmt.Sprintf("a yes to remove %d stopped %s", removable, noun),
	)
	if err != nil {
		return err
	}
	if !ok {
		return cancelled()
	}
	return nil
}

func forgetRemovedSessions(results []pruneHostJSON) {
	for _, r := range results {
		for _, s := range r.Sessions {
			if s.Action == "removed" {
				client.ForgetSession(r.Host, s.ID)
			}
		}
	}
}

func resolvePruneHosts(args []string) ([]client.Host, bool, error) {
	if len(args) == 1 {
		h, err := client.FindHost(args[0])
		if err != nil {
			return nil, true, err
		}
		return []client.Host{h}, true, nil
	}
	hosts, err := client.LoadHosts()
	if err != nil {
		return nil, false, err
	}
	if len(hosts) == 0 {
		return nil, false, &client.UserError{Msg: "no hosts yet", Hint: brand.Name + " host add <name>", Code: "no_hosts"}
	}
	return hosts, false, nil
}

func queryPruneHosts(ctx context.Context, hosts []client.Host, singleHost bool, req api.PruneArgs) ([]pruneHostJSON, error) {
	results := make([]pruneHostJSON, 0, len(hosts))
	var firstErr error
	answered := 0
	for _, h := range hosts {
		hctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		dialCtx := hctx
		if !singleHost {
			dialCtx = client.WithoutWake(hctx)
		}
		conn, err := client.Dial(dialCtx, h, false)
		if err != nil {
			cancel()
			if singleHost {
				return nil, err
			}
			if firstErr == nil {
				firstErr = err
			}
			results = append(results, pruneHostJSON{Host: h.Name, DryRun: req.DryRun, Sessions: []api.PruneSession{}, Error: err.Error()})
			continue
		}
		res, err := conn.Prune(hctx, req)
		conn.Close()
		cancel()
		if err != nil {
			if singleHost {
				return nil, err
			}
			if firstErr == nil {
				firstErr = err
			}
			results = append(results, pruneHostJSON{Host: h.Name, DryRun: req.DryRun, Sessions: []api.PruneSession{}, Error: err.Error()})
			continue
		}
		answered++
		sessions := res.Sessions
		if sessions == nil {
			sessions = []api.PruneSession{}
		}
		results = append(results, pruneHostJSON{
			Host:          h.Name,
			DryRun:        req.DryRun,
			Sessions:      sessions,
			RemovedImages: res.RemovedImages,
		})
	}
	if answered == 0 && firstErr != nil {
		return nil, firstErr
	}
	return results, nil
}

func printPruneTable(w *os.File, results []pruneHostJSON, dryRun bool) {
	anyRows := false
	for _, r := range results {
		host := shown(r.Host)
		if r.Error != "" {
			fmt.Fprintf(w, "%s  ◌ %s\n", host, shown(r.Error))
			anyRows = true
			continue
		}
		for _, s := range r.Sessions {
			anyRows = true
			name := s.Name
			if name == "" {
				name = s.ID
			}
			status := pruneStatusText(s)
			stopped := "-"
			if !s.StoppedAt.IsZero() {
				stopped = age(s.StoppedAt)
			}
			fmt.Fprintf(w, "%-14s %-10s %-20s %6s  %s\n", host, shown(s.ID), trunc(shown(name), 20), stopped, status)
		}
		for _, img := range r.RemovedImages {
			anyRows = true
			fmt.Fprintf(w, "%-14s image      %s  removed\n", host, shown(img))
		}
	}
	if !anyRows && dryRun {
		fmt.Fprintln(w, "No stopped sessions to remove.")
	}
}

func pruneStatusText(s api.PruneSession) string {
	switch s.Action {
	case "would_remove":
		return "would remove"
	case "removed":
		return "removed"
	case "kept":
		if s.Reason != "" {
			return "kept (" + shown(s.Reason) + ")"
		}
		return "kept"
	case "error":
		return "error (" + shown(s.Error) + ")"
	default:
		return shown(s.Action)
	}
}
