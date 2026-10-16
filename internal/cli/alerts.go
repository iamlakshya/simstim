package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

func hostAlertsCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "alerts <host>",
		Short: "Show where a host sends alerts about its sessions, and the last deliveries",
		Long: "A host can tell you when a session needs you, finishes a long turn or fails, with the laptop shut.\n" +
			"It sends events to destinations you name: an ntfy topic, a webhook or a command on the host.\n" +
			"Nothing is sent until you name one. See docs/how-to/alerts.md.",
		Example: "  " + brand.Name + " host alerts my-vps\n" +
			"  " + brand.Name + " host alerts set my-vps --ntfy https://ntfy.sh/<your-topic>\n" +
			"  " + brand.Name + " host alerts test my-vps",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withHost(cmd.Context(), args[0], func(ctx context.Context, h client.Host, conn *client.Conn) error {
				info, err := conn.Alerts(ctx)
				if err != nil {
					return err
				}
				log, err := conn.AlertsLog(ctx, 5)
				if err != nil {
					return err
				}
				if asJSON {
					return printJSON(map[string]any{"alerts": info, "log": log})
				}
				printAlerts(h.Name, info)
				if len(log) > 0 {
					fmt.Println("\nLast deliveries:")
					printAlertLog(log)
				}
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	cmd.AddCommand(alertsSetCmd(), alertsTestCmd(), alertsLogCmd(), alertsOffCmd())
	return cmd
}

// withHost connects to a saved host and runs fn.
func withHost(ctx context.Context, name string, fn func(context.Context, client.Host, *client.Conn) error) error {
	h, err := client.FindHost(name)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	conn, err := client.Dial(ctx, h, true)
	if err != nil {
		return err
	}
	defer conn.Close()
	return fn(ctx, h, conn)
}

type alertsSetFlags struct {
	ntfy, webhook, format, command, tokenCmd string
	events                                   []string
	delay, minTurn                           time.Duration
	detail, add                              bool
}

func alertsSetCmd() *cobra.Command {
	var f alertsSetFlags
	cmd := &cobra.Command{
		Use:   "set <host>",
		Short: "Choose where a host sends alerts, and which ones",
		Long: "Writes the host's alerts file (mode 0600). A destination flag replaces the file's destinations,\n" +
			"unless you add --add. With no destination flag, only the settings you give change.\n" +
			"--token-cmd runs a command here, such as a keychain read, and sends what it prints as the ntfy token.",
		Example: "  " + brand.Name + " host alerts set my-vps --ntfy https://ntfy.sh/<your-topic>\n" +
			"  " + brand.Name + " host alerts set my-vps --ntfy https://ntfy.example.com/agents --token-cmd 'security find-generic-password -s ntfy -w'\n" +
			"  " + brand.Name + " host alerts set my-vps --add --webhook https://hooks.slack.com/services/… --format slack --events failed,oom\n" +
			"  " + brand.Name + " host alerts set my-vps --add --command 'notify-send \"$" + brand.Env("SUMMARY") + "\"'\n" +
			"  " + brand.Name + " host alerts set my-vps --delay 30s --min-turn 5m",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, add, err := f.config(cmd)
			if err != nil {
				return err
			}
			if f.tokenCmd != "" {
				tok, err := client.SecretFromCommand(cmd.Context(), f.tokenCmd)
				if err != nil {
					return err
				}
				cfg.Destinations[0].Token = tok
			}
			return withHost(cmd.Context(), args[0], func(ctx context.Context, h client.Host, conn *client.Conn) error {
				info, err := conn.SetAlerts(ctx, cfg, add)
				if err != nil {
					return err
				}
				printAlerts(h.Name, info)
				fmt.Printf("\nSend a test alert with `%s host alerts test %s`.\n", brand.Name, h.Name)
				return nil
			})
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.ntfy, "ntfy", "", "send to this ntfy topic (https://ntfy.sh/<topic> or your own server)")
	fl.StringVar(&f.webhook, "webhook", "", "POST to this web address")
	fl.StringVar(&f.format, "format", "", "the webhook's body: json (the event), slack or discord")
	fl.StringVar(&f.command, "command", "", "run this command line on the host, with the event on stdin and in the environment")
	fl.StringVar(&f.tokenCmd, "token-cmd", "", "run this here and use its output as the ntfy token (it is never printed)")
	fl.StringSliceVar(&f.events, "events", nil, "the events to send: needs_input, turn_done, exited, failed, oom, memory, started, restored")
	fl.DurationVar(&f.delay, "delay", 0, "how long needs_input waits before it is sent (default 10s)")
	fl.DurationVar(&f.minTurn, "min-turn", 0, "the shortest turn that sends turn_done (default 1m)")
	fl.BoolVar(&f.detail, "detail", false, "add the agent's question to needs_input (it can hold code)")
	fl.BoolVar(&f.add, "add", false, "keep the host's destinations, and add this one")
	return cmd
}

// config builds the alerts config the flags ask for, and whether it adds to
// the host's file rather than replacing it.
func (f *alertsSetFlags) config(cmd *cobra.Command) (client.AlertsConfig, bool, error) {
	var c client.AlertsConfig
	fl := cmd.Flags()
	if f.tokenCmd != "" && f.ntfy == "" {
		return c, false, &client.UserError{Msg: "--token-cmd needs --ntfy", Hint: "Give the ntfy topic with --ntfy."}
	}
	if f.format != "" && f.webhook == "" {
		return c, false, &client.UserError{Msg: "--format needs --webhook", Hint: "Give the address with --webhook."}
	}
	if f.ntfy != "" {
		c.Destinations = append(c.Destinations, client.AlertDestination{Type: "ntfy", URL: f.ntfy})
	}
	if f.webhook != "" {
		c.Destinations = append(c.Destinations, client.AlertDestination{Type: "webhook", URL: f.webhook, Format: f.format})
	}
	if f.command != "" {
		c.Destinations = append(c.Destinations, client.AlertDestination{Type: "command", Run: f.command})
	}
	if fl.Changed("events") {
		for _, e := range f.events {
			c.Events = append(c.Events, strings.TrimSpace(e))
		}
	}
	if fl.Changed("delay") {
		c.Delay = f.delay.String()
	}
	if fl.Changed("min-turn") {
		c.MinTurn = f.minTurn.String()
	}
	if fl.Changed("detail") {
		c.Detail = &f.detail
	}
	if len(c.Destinations) == 0 && !fl.Changed("events") && !fl.Changed("delay") && !fl.Changed("min-turn") && !fl.Changed("detail") {
		return c, false, &client.UserError{Msg: "nothing to set", Hint: "Name a destination with --ntfy, --webhook or --command, or change --events, --delay, --min-turn or --detail."}
	}
	return c, f.add || len(c.Destinations) == 0, nil
}

func alertsTestCmd() *cobra.Command {
	var event string
	cmd := &cobra.Command{
		Use:   "test <host>",
		Short: "Send a test alert to every destination now, and say what each did",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withHost(cmd.Context(), args[0], func(ctx context.Context, h client.Host, conn *client.Conn) error {
				res, err := conn.TestAlerts(ctx, event)
				if err != nil {
					return err
				}
				fmt.Printf("Sent a test alert from %s:\n", h.Name)
				failed := 0
				for _, r := range res {
					mark, what := "✓", fmt.Sprintf("%d ms", r.MS)
					if !r.OK {
						mark, what = "✕", r.Error
						failed++
					}
					fmt.Printf("  %s %-8s %s  %s\n", mark, r.Type, r.Target, what)
				}
				if failed > 0 {
					return &client.UserError{Msg: fmt.Sprintf("%d of %d destinations did not get it", failed, len(res)),
						Hint: brand.Name + " host alerts log " + h.Name}
				}
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&event, "event", "test", "the kind of event to send, such as needs_input or failed")
	return cmd
}

func alertsLogCmd() *cobra.Command {
	var n int
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "log <host>",
		Short: "Show the host's last alert deliveries, and why any were not sent",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withHost(cmd.Context(), args[0], func(ctx context.Context, h client.Host, conn *client.Conn) error {
				log, err := conn.AlertsLog(ctx, n)
				if err != nil {
					return err
				}
				if asJSON {
					return printJSON(log)
				}
				if len(log) == 0 {
					fmt.Printf("%s has sent no alerts yet.\n", h.Name)
					return nil
				}
				printAlertLog(log)
				return nil
			})
		},
	}
	cmd.Flags().IntVarP(&n, "lines", "n", 20, "how many lines to show")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

func alertsOffCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "off <host>",
		Short: "Turn alerts off on a host: remove its alerts file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withHost(cmd.Context(), args[0], func(ctx context.Context, h client.Host, conn *client.Conn) error {
				removed, err := conn.AlertsOff(ctx)
				if err != nil {
					return err
				}
				if removed {
					fmt.Printf("Alerts on %s are off: its alerts file is gone.\n", h.Name)
				} else {
					fmt.Printf("Alerts on %s were already off.\n", h.Name)
				}
				return nil
			})
		},
	}
}

// printAlerts shows a host's alerts file in the style of host doctor.
func printAlerts(host string, info client.AlertsInfo) {
	if !info.Exists {
		fmt.Printf("Alerts on %s are off: there is no %s.\n", host, info.Path)
		fmt.Printf("    Try: %s host alerts set %s --ntfy https://ntfy.sh/<your-topic>\n", brand.Name, host)
		return
	}
	if info.Error != "" {
		fmt.Printf("Alerts on %s are off: %s cannot be used.\n", host, info.Path)
		fmt.Printf("  ✕ %s\n", info.Error)
		if info.Hint != "" {
			fmt.Printf("    Try: %s\n", info.Hint)
		}
	} else {
		fmt.Printf("Alerts on %s are on (%s).\n", host, info.Path)
	}
	c := info.Config
	if c == nil {
		return
	}
	fmt.Printf("  events   %s\n", strings.Join(c.Events, ", "))
	fmt.Printf("  timing   needs_input waits %s · turn_done for turns of %s or more\n", c.Delay, c.MinTurn)
	fmt.Printf("  privacy  detail %s · needs_input %s\n", onOff(c.Detail != nil && *c.Detail),
		map[bool]string{true: "stays quiet while you type", false: "is sent even while you type"}[c.QuietWhenTyping == nil || *c.QuietWhenTyping])
	if len(c.Destinations) == 0 {
		fmt.Printf("  ✕ no destination\n    Try: %s host alerts set %s --add --ntfy https://ntfy.sh/<your-topic>\n", brand.Name, host)
	}
	for _, d := range c.Destinations {
		fmt.Printf("  → %-8s %s%s\n", d.Type, destinationText(d), destinationNotes(d))
	}
}

func destinationText(d client.AlertDestination) string {
	if d.Type == "command" {
		return d.Run
	}
	return d.URL
}

func destinationNotes(d client.AlertDestination) string {
	var notes []string
	switch {
	case d.Token != "":
		notes = append(notes, "token set")
	case d.TokenFile != "":
		notes = append(notes, "token from "+d.TokenFile)
	}
	if d.Format != "" && d.Format != "json" {
		notes = append(notes, d.Format)
	}
	if d.Priority != "" {
		notes = append(notes, "priority "+d.Priority)
	}
	if len(d.Headers) > 0 {
		notes = append(notes, fmt.Sprintf("%d headers", len(d.Headers)))
	}
	if len(d.Events) > 0 {
		notes = append(notes, "only "+strings.Join(d.Events, ", "))
	}
	if len(notes) == 0 {
		return ""
	}
	return "  (" + strings.Join(notes, ", ") + ")"
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// printAlertLog prints delivery log lines, oldest first, in local time.
func printAlertLog(log []client.AlertLogEntry) {
	for _, e := range log {
		var result string
		switch {
		case e.Skipped != "":
			result = "not sent: " + e.Skipped
		case e.OK:
			result = fmt.Sprintf("✓ %s, %d ms", e.Type, e.MS)
		default:
			result = "✕ " + strings.TrimSpace(e.Type+" "+e.Error)
		}
		name := e.Session
		if name == "" {
			name = "-"
		}
		fmt.Printf("  %s  %-11s %-16s %s\n", e.Time.Local().Format("Jan 02 15:04:05"), e.Event, trunc(name, 16), result)
	}
}
