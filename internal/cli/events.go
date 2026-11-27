package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/alerts"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

func eventsCmd() *cobra.Command {
	var follow, asJSON bool
	cmd := &cobra.Command{
		Use:     "events [<host>|<host>/<session>]",
		Short:   "Read recent session events, or follow them as they happen",
		Long:    "Reads recent events from one session, one host, or every saved host. --follow keeps the stream open.\nThe connection uses the host's existing SSH route. It never polls or starts a stopped host.\nJSON output is one versioned event per line.",
		Example: "  " + brand.Name + " events my-vps\n  " + brand.Name + " events my-vps/auth-fix --follow\n  " + brand.Name + " events --follow --json",
		Args:    cobra.MaximumNArgs(1),
		ValidArgsFunction: func(c *cobra.Command, args []string, word string) ([]string, cobra.ShellCompDirective) {
			if strings.Contains(word, "/") {
				return completeSessionArg(c, args, word)
			}
			return completeLsHost(c, args, word)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := client.WithoutWake(cmd.Context())
			if len(args) == 1 && strings.Contains(args[0], "/") {
				a, err := resolveAddress(cmd, args[0], false)
				if err != nil {
					return err
				}
				return a.open(ctx, cmd, func(ctx context.Context, f found) error {
					return f.conn.WatchEvents(ctx, api.EventsWatchArgs{V: 1, ID: f.s.ID, Follow: follow}, func(ev alerts.Event) error { return writeEvent(f.host.Name, ev, asJSON) })
				})
			}
			hosts, err := client.LoadHosts()
			if err != nil {
				return err
			}
			if len(args) == 1 {
				h, err := client.FindHost(args[0])
				if err != nil {
					return err
				}
				hosts = []client.Host{h}
			}
			if len(hosts) == 0 {
				return &client.UserError{Msg: "no hosts yet", Hint: brand.Name + " host add <name>", Code: "no_hosts"}
			}
			return watchHostEvents(ctx, cmd, hosts, follow, asJSON)
		},
	}
	addFollow(cmd, &follow, "keep reading events until interrupted")
	addJSON(cmd, &asJSON)
	return cmd
}

func watchHostEvents(ctx context.Context, cmd *cobra.Command, hosts []client.Host, follow, asJSON bool) error {
	var output sync.Mutex
	var workers sync.WaitGroup
	errors := make([]error, len(hosts))
	for i, h := range hosts {
		workers.Add(1)
		go func() {
			defer workers.Done()
			start, cancel := context.WithTimeout(ctx, 60*time.Second)
			c, err := client.Dial(start, h, false)
			cancel()
			if err == nil {
				defer c.Close()
				err = c.WatchEvents(ctx, api.EventsWatchArgs{V: 1, Follow: follow}, func(ev alerts.Event) error {
					output.Lock()
					defer output.Unlock()
					return writeEvent(h.Name, ev, asJSON)
				})
			}
			errors[i] = err
			if follow && err != nil && ctx.Err() == nil {
				r := describe(err, cmd)
				output.Lock()
				warningFor(asJSON)(r.code, "events on "+h.Name+": "+r.msg, r.try)
				output.Unlock()
			}
		}()
	}
	workers.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, err := range errors {
		if err != nil {
			return err
		}
	}
	return nil
}

func writeEvent(host string, ev alerts.Event, asJSON bool) error {
	if asJSON {
		line, err := json.Marshal(ev)
		if err != nil {
			return fmt.Errorf("encode event: %w", err)
		}
		if _, err := os.Stdout.Write(append(jsonSafe(line), '\n')); err != nil {
			return fmt.Errorf("write event: %w", err)
		}
		return nil
	}
	address := host
	if ev.Session.ID != "" {
		address += "/" + ev.Session.ID
	}
	if _, err := fmt.Fprintf(os.Stdout, "%s %s %s %s\n", shown(ev.Time), shown(address), shown(ev.Event), shown(ev.Summary)); err != nil {
		return fmt.Errorf("write event: %w", err)
	}
	return nil
}
