package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
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
	dctx, cancel := context.WithTimeout(client.WithoutInstall(client.WithoutWake(ctx)), 30*time.Second)
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
		// The launcher does not ask for agent_checks, so host show omits them too (cli-spec 11.2, rule 4).
		d.AgentChecks = nil
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
	det := client.BuildHostDetails(h, st.info, st.sessionCount)
	printHostDetails(os.Stdout, det)
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
		printMissingHostPackages(rows, hints, *sandbox)
		return
	}
	if !hasSandbox {
		fmt.Fprintf(rows, "\nSandboxes on %s:\n  ✕ the helper on %s is too old for sandboxes\n", h.Name, h.Name)
		fmt.Fprintf(hints, "    Try: %s host upgrade %s\n", brand.Name, client.ShellQuote(h.Name))
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

func printMissingHostPackages(rows, hints io.Writer, p api.CapProbe) {
	if p.OS != "linux" || !p.KVM.Present {
		return
	}
	var pkgs []string
	if !p.HasSetfacl && !p.KVM.Usable {
		pkgs = append(pkgs, "acl")
	}
	if !p.HasNewuidmap {
		pkgs = append(pkgs, "uidmap")
	}
	if len(pkgs) == 0 {
		return
	}
	fmt.Fprintf(rows, "\nOptional host packages missing: %s\n", strings.Join(pkgs, ", "))
	fmt.Fprintf(hints, "  Try: sudo apt install %s\n", strings.Join(pkgs, " "))
}

func showHostJSON(h client.Host, reachable bool, dialErr error, info *client.HostInfo, sandbox *api.CapProbe, sandboxErr error, sessionCount int) error {
	obj, _ := tomlKeys(reflect.ValueOf(h)).(map[string]any)
	if obj == nil {
		obj = map[string]any{}
	}
	obj["trusted"] = h.Trusted
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
			cp := *info
			cp.AgentChecks = nil
			obj["info"] = &cp
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

func printHostDetails(w io.Writer, d client.HostDetails) {
	fmt.Fprintf(w, "%s\n", d.Name)
	fmt.Fprintf(w, "  Target      %s\n", d.Target)
	if d.SSHConfig != "" {
		fmt.Fprintf(w, "  SSH config  %s\n", d.SSHConfig)
	}
	trusted := "no"
	if d.Trusted {
		trusted = "yes"
	}
	fmt.Fprintf(w, "  Trusted     %s\n", trusted)
	if d.WallFloor != "" {
		fmt.Fprintf(w, "  Floor       %s\n", d.WallFloor)
	}
	if d.Machine != nil {
		fmt.Fprintf(w, "  Machine     %s (%s)\n", d.Machine.Provider, d.Machine.ID)
		if d.Machine.IdleStop != "" {
			fmt.Fprintf(w, "  Idle stop   %s\n", d.Machine.IdleStop)
		}
	}
	if len(d.Labels) > 0 {
		fmt.Fprintf(w, "  Labels      %s\n", strings.Join(d.Labels, ", "))
	}
	if len(d.Secrets) > 0 {
		fmt.Fprintf(w, "  Secrets     %s\n", strings.Join(d.Secrets, ", "))
	}
	if !d.Reachable {
		return
	}
	if d.System != "" {
		fmt.Fprintf(w, "  System      %s\n", d.System)
	}
	if d.Helper != "" {
		fmt.Fprintf(w, "  Helper      %s\n", d.Helper)
	}
	if len(d.Agents) > 0 {
		fmt.Fprintf(w, "  Agents      %s\n", strings.Join(d.Agents, ", "))
	}
	if d.Limits != "" {
		fmt.Fprintf(w, "  Limits      %s\n", d.Limits)
	}
}
