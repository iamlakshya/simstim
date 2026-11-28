package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

func eventsCmd() *cobra.Command {
	var follow, asJSON bool
	cmd := &cobra.Command{
		Use:   "events [<host>|<host>/<session>]",
		Short: "Show or follow events from sessions and sandboxes",
		Long: "Reads session and sandbox events from one host, one session or every saved host. With\n" +
			"--follow, holds one stream open per host and prints new events as they happen, without\n" +
			"polling. With --follow --json, prints one compact JSON object per line.",
		Example: "  " + brand.Name + " events my-vps\n" +
			"  " + brand.Name + " events my-vps/auth-fix --follow\n" +
			"  " + brand.Name + " events --follow --json",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runEvents(cmd, args, follow, asJSON)
		},
	}
	addFollow(cmd, &follow, "keep watching and print new events as they happen")
	addJSON(cmd, &asJSON)
	return cmd
}

func completeEventsArg(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) != 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	if strings.Contains(toComplete, "/") {
		return completeSessions(cmd, args, toComplete)
	}
	hosts, _ := completeHosts(cmd, args, toComplete)
	sessions := cachedSessionCompletions(toComplete, "")
	return append(hosts, sessions...), cobra.ShellCompDirectiveNoFileComp
}

func runEvents(cmd *cobra.Command, args []string, follow, asJSON bool) error {
	hosts, sessionID, singleHost, err := resolveEventsTarget(cmd, args)
	if err != nil {
		return err
	}
	req := api.EventsWatchArgs{
		Session: sessionID,
		Follow:  follow,
	}
	if !follow {
		return runEventsSnapshot(cmd.Context(), hosts, singleHost, req, asJSON)
	}
	return runEventsFollow(cmd.Context(), hosts, singleHost, req, asJSON)
}

func resolveEventsTarget(cmd *cobra.Command, args []string) (hosts []client.Host, sessionID string, singleHost bool, err error) {
	if len(args) == 0 {
		hosts, err = client.LoadHosts()
		if err != nil {
			return nil, "", false, err
		}
		if len(hosts) == 0 {
			return nil, "", false, &client.UserError{Msg: "no hosts yet", Hint: brand.Name + " host add <name>", Code: "no_hosts"}
		}
		return hosts, "", false, nil
	}
	if strings.Contains(args[0], "/") {
		a, err := resolveAddress(cmd, args[0], false)
		if err != nil {
			return nil, "", true, err
		}
		var targetHost client.Host
		var targetID string
		err = a.open(cmd.Context(), cmd, func(_ context.Context, f found) error {
			targetHost = f.host
			targetID = f.s.ID
			return nil
		})
		if err != nil {
			return nil, "", true, err
		}
		return []client.Host{targetHost}, targetID, true, nil
	}
	h, err := client.FindHost(args[0])
	if err != nil {
		return nil, "", true, err
	}
	return []client.Host{h}, "", true, nil
}

func runEventsSnapshot(ctx context.Context, hosts []client.Host, singleHost bool, req api.EventsWatchArgs, asJSON bool) error {
	var mu sync.Mutex
	collected := make([]api.WatchEvent, 0)
	var firstErr error
	answered := 0

	var wg sync.WaitGroup
	for _, h := range hosts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			hctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			watchCtx := hctx
			if !singleHost {
				watchCtx = client.WithoutWake(hctx)
			}
			var hostEvents []api.WatchEvent
			err := client.WatchEvents(watchCtx, h, req, func(ev api.WatchEvent) error {
				hostEvents = append(hostEvents, ev)
				return nil
			})
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				return
			}
			answered++
			collected = append(collected, hostEvents...)
		}()
	}
	wg.Wait()
	if answered == 0 && firstErr != nil {
		return firstErr
	}
	sort.SliceStable(collected, func(i, j int) bool {
		return collected[i].Time.Before(collected[j].Time)
	})
	if asJSON {
		for i := range collected {
			collected[i].V = 0 // printJSON inserts "v": 1 on each array element
		}
		return printJSON(collected)
	}
	for _, ev := range collected {
		fmt.Fprintln(os.Stdout, formatEventLine(ev, time.Local))
	}
	return nil
}

func runEventsFollow(ctx context.Context, hosts []client.Host, singleHost bool, req api.EventsWatchArgs, asJSON bool) error {
	var outMu sync.Mutex
	emit := func(ev api.WatchEvent) error {
		outMu.Lock()
		defer outMu.Unlock()
		if asJSON {
			return writeEventJSONLine(os.Stdout, ev)
		}
		if _, err := fmt.Fprintln(os.Stdout, formatEventLine(ev, time.Local)); err != nil {
			return fmt.Errorf("write event line: %w", err)
		}
		return nil
	}
	if len(hosts) == 1 {
		err := client.WatchEvents(ctx, hosts[0], req, emit)
		if isCtxDone(ctx) {
			return nil
		}
		return err
	}

	var mu sync.Mutex
	var firstErr error
	okAny := false
	var wg sync.WaitGroup
	for _, h := range hosts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			watchCtx := ctx
			if !singleHost {
				watchCtx = client.WithoutWake(ctx)
			}
			err := client.WatchEvents(watchCtx, h, req, emit)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				return
			}
			okAny = true
		}()
	}
	wg.Wait()
	if isCtxDone(ctx) {
		return nil
	}
	if !okAny && firstErr != nil {
		return firstErr
	}
	return nil
}

func isCtxDone(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return true
	default:
		return false
	}
}

func writeEventJSONLine(w io.Writer, ev api.WatchEvent) error {
	ev.V = jsonVersion
	line, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("encode event JSON: %w", err)
	}
	if _, err := fmt.Fprintf(w, "%s\n", jsonSafe(line)); err != nil {
		return fmt.Errorf("write event JSON: %w", err)
	}
	return nil
}

// formatEventLine renders one human-readable line for `events` with every
// host-supplied field cleaned for terminal output (spec 3.2, 17.6).
func formatEventLine(ev api.WatchEvent, loc *time.Location) string {
	ts := "--:--:--"
	if !ev.Time.IsZero() {
		if loc == nil {
			loc = time.UTC
		}
		ts = ev.Time.In(loc).Format("15:04:05")
	}
	who := eventTargetLabel(ev)
	kind := shown(ev.Event)
	summary := eventSummary(ev)
	if summary == "" {
		return fmt.Sprintf("%s  %-22s  %s", ts, trunc(who, 22), kind)
	}
	return fmt.Sprintf("%s  %-22s  %-16s  %s", ts, trunc(who, 22), kind, summary)
}

func eventTargetLabel(ev api.WatchEvent) string {
	host := shown(ev.Host)
	sess := shown(ev.Name)
	if sess == "" {
		sess = shown(ev.Session)
	}
	switch {
	case host != "" && sess != "":
		return host + "/" + sess
	case host != "":
		return host
	case sess != "":
		return sess
	default:
		return "-"
	}
}

func eventSummary(ev api.WatchEvent) string {
	switch ev.Event {
	case api.EventStateChanged:
		st := shown(ev.State)
		if prev := shown(ev.Prev); prev != "" {
			return fmt.Sprintf("%s (from %s)", st, prev)
		}
		return st
	case api.EventNeedsInput:
		if d := shown(ev.Detail); d != "" {
			return d
		}
		return "waiting for input"
	case api.EventTurnDone:
		if d := shown(ev.Detail); d != "" {
			return d
		}
		return "turn finished"
	case api.EventEnded:
		if ev.Signal != "" {
			return "ended by signal " + shown(ev.Signal)
		}
		if ev.ExitCode != nil {
			return fmt.Sprintf("exited (code %d)", *ev.ExitCode)
		}
		if r := shown(ev.Reason); r != "" {
			return r
		}
		return "ended"
	case api.EventEgressHeld, api.EventClassifierAllow:
		target := shown(ev.Target)
		detail := shown(ev.Detail)
		if detail == "" {
			detail = shown(ev.Reason)
		}
		if target != "" && detail != "" {
			return target + "  " + detail
		}
		if target != "" {
			return target
		}
		return detail
	case api.EventSandboxReady:
		if d := shown(ev.Detail); d != "" {
			return d
		}
		return "sandbox ready"
	case api.EventSandboxStopped:
		if r := shown(ev.Reason); r != "" {
			return r
		}
		return "sandbox stopped"
	case api.EventSandboxError:
		if d := shown(ev.Detail); d != "" {
			return d
		}
		if r := shown(ev.Reason); r != "" {
			return r
		}
		return "sandbox error"
	default:
		if d := shown(ev.Detail); d != "" {
			return d
		}
		return shown(ev.Reason)
	}
}
