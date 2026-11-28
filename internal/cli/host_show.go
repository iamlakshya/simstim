package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/wall"
)

func hostShowCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "show <host>",
		Short: "Show a host's settings, sandbox levels and status",
		Long: "Shows the saved settings of a host and dials it without waking it to check its\n" +
			"system, agents and sandbox drivers. A host that does not answer prints its saved\n" +
			"settings and the reason, and exits with 0.",
		Example: "  " + brand.Name + " host show my-vps\n  " + brand.Name + " host show my-vps --json",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			h, err := client.FindHost(args[0])
			if err != nil {
				return err
			}
			state, err := fetchHostShow(cmd.Context(), h)
			if err != nil {
				return err
			}
			return renderHostShow(h, state, asJSON)
		},
	}
	addJSON(cmd, &asJSON)
	return cmd
}

type hostShowState struct {
	reachable    bool
	dialErr      error
	info         *client.HostInfo
	hasSandbox   bool
	sandbox      *api.CapProbe
	sandboxErr   error
	sessionCount int
}

func fetchHostShow(ctx context.Context, h client.Host) (hostShowState, error) {
	dctx, cancel := context.WithTimeout(client.WithoutWake(ctx), 30*time.Second)
	defer cancel()

	var st hostShowState
	conn, err := client.Dial(dctx, h, false)
	if err != nil {
		st.dialErr = err
		return st, nil
	}
	defer conn.Close()
	st.reachable = true
	if d, err := conn.Doctor(dctx); err == nil {
		st.info = &d
	}
	if list, err := conn.Sessions(dctx); err == nil {
		st.sessionCount = len(list)
	}
	st.hasSandbox = conn.HasFeature("sandbox")
	if st.hasSandbox {
		p, err := conn.CapProbe(dctx, "")
		switch {
		case err == nil:
			st.sandbox = &p
		case errors.Is(err, context.Canceled) || stopped(ctx):
			return st, err
		default:
			st.sandboxErr = err
		}
	}
	return st, nil
}

func renderHostShow(h client.Host, st hostShowState, asJSON bool) error {
	if asJSON {
		return showHostJSON(h, st.reachable, st.dialErr, st.info, st.sandbox, st.sandboxErr, st.sessionCount)
	}
	printHostSaved(os.Stdout, h)
	if !st.reachable {
		r := describe(st.dialErr, nil)
		printf("\n%s did not answer: %s\n", h.Name, r.msg)
		try := r.try
		if try == "" || try == debugHint {
			try = brand.Name + " host doctor " + client.ShellQuote(h.Name)
		}
		fmt.Fprintf(os.Stderr, "Try: %s\n", try)
		return nil
	}
	if st.info != nil {
		clean := client.CleanHostInfo(*st.info)
		printf("  System      %s (%s/%s), %d CPUs, %.1f GiB\n", clean.Distro, clean.OS, clean.Arch, clean.CPUs, float64(clean.MemTotal)/(1<<30))
		printf("  Helper      %s\n", clean.Version)
		printf("  Sessions    %d\n", st.sessionCount)
		if len(clean.Agents) > 0 {
			printf("  Agents      %s\n", formatAgentSummary(clean.Agents, clean.AgentVersions))
		}
	}
	printHostSandboxSection(os.Stdout, os.Stderr, h, st.hasSandbox, st.sandbox, st.sandboxErr)
	return nil
}

func printHostSandboxSection(rows, hints io.Writer, h client.Host, hasSandbox bool, sandbox *api.CapProbe, sandboxErr error) {
	if sandbox != nil {
		fmt.Fprintln(rows)
		printSandboxesBlock(rows, hints, h.Name, *sandbox, h.WallFloor)
		if len(sandbox.Walls) > 0 {
			fmt.Fprintf(rows, "\nWall drivers on %s:\n", h.Name)
			printWallDrivers(rows, hints, sandbox.Walls)
		}
		return
	}
	if !hasSandbox {
		fmt.Fprintf(rows, "\nSandboxes on %s:\n  ✕ the helper on %s is too old for sandboxes\n", h.Name, h.Name)
		fmt.Fprintf(hints, "    Try: %s host add %s\n", brand.Name, client.ShellQuote(h.Name))
		return
	}
	r := describe(sandboxErr, nil)
	msg := r.msg
	if msg == "" {
		msg = "unknown error"
	}
	fmt.Fprintf(rows, "\nSandboxes on %s:\n  ✕ sandbox probe failed: %s\n", h.Name, msg)
	try := r.try
	if try == "" || try == debugHint {
		try = brand.Name + " host doctor " + client.ShellQuote(h.Name)
	}
	fmt.Fprintf(hints, "    Try: %s\n", try)
}

func showHostJSON(h client.Host, reachable bool, dialErr error, info *client.HostInfo, sandbox *api.CapProbe, sandboxErr error, sessionCount int) error {
	obj, _ := tomlKeys(reflect.ValueOf(h)).(map[string]any)
	if obj == nil {
		obj = map[string]any{}
	}
	obj["reachable"] = reachable
	if !reachable && dialErr != nil {
		r := describe(dialErr, nil)
		obj["error"] = r.msg
		try := r.try
		if try == "" || try == debugHint {
			try = brand.Name + " host doctor " + client.ShellQuote(h.Name)
		}
		obj["try"] = try
	} else {
		obj["session_count"] = sessionCount
		if info != nil {
			obj["info"] = info
		}
		if sandbox != nil {
			obj["sandbox"] = sandbox
		} else if sandboxErr != nil {
			r := describe(sandboxErr, nil)
			obj["sandbox_error"] = r.msg
		}
	}
	return printJSON(obj)
}

func printHostSaved(w io.Writer, h client.Host) {
	fmt.Fprintf(w, "%s\n", h.Name)
	fmt.Fprintf(w, "  Target      %s\n", h.Target())
	if h.SSHConfig != "" {
		fmt.Fprintf(w, "  SSH config  %s\n", h.SSHConfig)
	}
	if h.WallFloor != "" {
		fmt.Fprintf(w, "  Floor       %s\n", h.WallFloor)
	}
	if h.Machine != nil {
		fmt.Fprintf(w, "  Machine     %s (%s)\n", h.Machine.Provider, h.Machine.ID)
		if h.Machine.IdleStop != "" {
			fmt.Fprintf(w, "  Idle stop   %s\n", h.Machine.IdleStop)
		}
	}
	if len(h.Labels) > 0 {
		keys := make([]string, 0, len(h.Labels))
		for k := range h.Labels {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, k+"="+h.Labels[k])
		}
		fmt.Fprintf(w, "  Labels      %s\n", strings.Join(parts, ", "))
	}
	if len(h.Secrets) > 0 {
		fmt.Fprintf(w, "  Secrets     %s\n", strings.Join(h.Secrets, ", "))
	}
}

func formatAgentSummary(found, versions map[string]string) string {
	names := make([]string, 0, len(found))
	for k := range found {
		if v := versions[k]; v != "" {
			k += " " + v
		}
		names = append(names, k)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func hostSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set <host> <key>=<value>…",
		Short: "Change named settings of a host",
		Long: "Changes one or more settings of a saved host. Supported keys:\n" +
			"  floor=<none|process|gvisor|microvm>  weakest sandbox level for new sessions\n" +
			"  idle=<duration>|never                idle stop for a created host",
		Example: "  " + brand.Name + " host set my-vps floor=process\n  " + brand.Name + " host set my-vps floor=none",
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			h, err := client.FindHost(args[0])
			if err != nil {
				return err
			}
			if len(args) < 2 {
				return &client.UserError{
					Msg:  "host set needs a setting, such as floor=process",
					Hint: brand.Name + " host show " + client.ShellQuote(h.Name),
					Code: "bad_value",
					Kind: client.KindUsage,
				}
			}
			return setHostSettings(cmd.Context(), cmd, h, args[1:])
		},
	}
}

type hostSetting struct {
	key, val string
}

func setHostSettings(ctx context.Context, cmd *cobra.Command, h client.Host, pairs []string) error {
	parsed, wantFloor, wantIdle, err := parseHostSettings(h, pairs)
	if err != nil {
		return err
	}
	if wantFloor != nil && *wantFloor > wall.None {
		if err := checkFloorSupported(ctx, h, *wantFloor); err != nil {
			return err
		}
	}

	oldFloor := h.WallFloor
	if oldFloor == "" {
		oldFloor = api.LevelNone
	}
	var oldIdle string
	if h.Machine != nil {
		oldIdle = h.Machine.IdleStop
	}

	if wantFloor != nil {
		h.WallFloor = wantFloor.String()
	}
	if wantIdle != nil {
		h.Machine.IdleStop = *wantIdle
	}
	if err := client.SaveHost(ctx, h); err != nil {
		return err
	}
	if wantIdle != nil && powersOffWhenIdle(h.Machine.Provider) {
		if err := applyIdle(ctx, h); err != nil {
			return err
		}
	}
	for _, s := range parsed {
		switch s.key {
		case "floor":
			say(cmd, "%s: floor %s → %s.\n", h.Name, oldFloor, s.val)
		case "idle":
			if oldIdle == "" {
				say(cmd, "%s: idle %s.\n", h.Name, s.val)
			} else {
				say(cmd, "%s: idle %s → %s.\n", h.Name, oldIdle, s.val)
			}
		}
	}
	return nil
}

func parseHostSettings(h client.Host, pairs []string) (parsed []hostSetting, wantFloor *wall.Level, wantIdle *string, err error) {
	parsed = make([]hostSetting, 0, len(pairs))
	for _, pair := range pairs {
		k, v, ok := strings.Cut(pair, "=")
		if !ok || k == "" {
			return nil, nil, nil, &client.UserError{
				Msg:  fmt.Sprintf("%q is not key=value", pair),
				Hint: brand.Name + " host set " + client.ShellQuote(h.Name) + " floor=process",
				Code: "bad_value",
				Kind: client.KindUsage,
			}
		}
		switch k {
		case "floor":
			lvl, perr := wall.ParseLevel(v)
			if perr != nil {
				return nil, nil, nil, &client.UserError{
					Msg:  fmt.Sprintf("floor: %v", perr),
					Hint: brand.Name + " host set " + client.ShellQuote(h.Name) + " floor=process",
					Code: "bad_value",
					Kind: client.KindUsage,
				}
			}
			wantFloor = &lvl
			parsed = append(parsed, hostSetting{key: k, val: lvl.String()})
		case "idle":
			if h.Machine == nil {
				return nil, nil, nil, &client.UserError{
					Msg:  h.Name + " was not made by `host create`",
					Hint: "Use a host made by " + brand.Name + " host create.",
					Code: "not_created",
				}
			}
			if v != "never" {
				if d, perr := time.ParseDuration(v); perr != nil || d < time.Minute {
					return nil, nil, nil, &client.UserError{
						Msg:  fmt.Sprintf("%q is not an idle time", v),
						Hint: "Use a duration of a minute or more, such as 30m or 2h, or never.",
						Code: "bad_value",
						Kind: client.KindUsage,
					}
				}
			}
			val := v
			wantIdle = &val
			parsed = append(parsed, hostSetting{key: k, val: val})
		default:
			return nil, nil, nil, unknownHostSetKey(h.Name, k)
		}
	}
	return parsed, wantFloor, wantIdle, nil
}

func checkFloorSupported(ctx context.Context, h client.Host, want wall.Level) error {
	dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	conn, err := client.Dial(dctx, h, true)
	if err != nil {
		return err
	}
	defer conn.Close()
	probe, err := conn.CapProbe(dctx, "")
	if err != nil {
		return err
	}
	bestName, _ := api.LevelsFor(probe, api.RuntimeStatus{})
	best, _ := wall.ParseLevel(bestName)
	if want > best {
		return &client.UserError{
			Msg:  fmt.Sprintf("%s can run up to %s, so its floor cannot be %s", h.Name, best, want),
			Hint: brand.Name + " host show " + client.ShellQuote(h.Name),
			Code: "wall_floor",
			Kind: client.KindRefused,
		}
	}
	return nil
}

func unknownHostSetKey(hostName, key string) error {
	known := []named{
		{name: "floor", answers: []string{"floor"}},
		{name: "idle", answers: []string{"idle"}},
	}
	sugs := suggest(key, known, false)
	return &usageError{
		UserError: &client.UserError{
			Msg:  fmt.Sprintf("host set has no key %q", key),
			Hint: brand.Name + " host set " + client.ShellQuote(hostName) + " floor=process",
			Code: "bad_value",
			Kind: client.KindUsage,
		},
		suggestions: sugs,
	}
}
