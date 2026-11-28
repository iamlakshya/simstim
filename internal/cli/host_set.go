package cli

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/wall"
)

func hostSetCmd() *cobra.Command {
	var confirm string
	cmd := &cobra.Command{
		Use:   "set <host> <key>=<value>…",
		Short: "Change named settings of a host",
		Long: "Changes one or more settings of a saved host. Supported keys:\n" +
			"  idle=<duration>|never                idle stop for a created host\n" +
			"  trusted=<true|false>                 whether you control root on this host\n" +
			"  floor=<none|process|gvisor|microvm>  weakest sandbox level for new sessions\n" +
			"  label.<key>=<value>                  set a label (empty value removes it)\n\n" +
			"Marking an untrusted host trusted checks its pinned SSH key and asks you to type\n" +
			"the host's name, or takes --confirm <host>.",
		Example: "  " + brand.Name + " host set my-vps idle=2h\n" +
			"  " + brand.Name + " host set my-vps floor=process\n" +
			"  " + brand.Name + " host set my-vps trusted=true --confirm my-vps",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) < 2 {
				h, err := client.FindHost(args[0])
				if err != nil {
					return err
				}
				return &client.UserError{
					Msg:  "host set needs a setting, such as idle=2h",
					Hint: brand.Name + " host show " + client.ShellQuote(h.Name),
					Code: "bad_value",
					Kind: client.KindUsage,
				}
			}
			return runHostSet(cmd.Context(), cmd, prompts(cmd), args[0], args[1:], confirm)
		},
	}
	addConfirm(cmd, &confirm, "type the host's name here instead of at the prompt when setting trusted=true")
	return cmd
}

func hostIdleCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "idle <host> <duration>|never",
		Short:  "Set how long a created host may idle before it stops",
		Hidden: true,
		Example: "  " + brand.Name + " host idle lima 30m\n" +
			"  " + brand.Name + " host idle lima never",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			warnOld(cmd, oldHostIdle)
			return runHostSet(cmd.Context(), cmd, prompts(cmd), args[0], []string{"idle=" + args[1]}, "")
		},
	}
	return cmd
}

type hostSetting struct {
	key, val string
	labelKey string
}

type parsedHostSettings struct {
	items       []hostSetting
	wantFloor   *wall.Level
	wantIdle    *string
	wantTrusted *bool
}

func runHostSet(ctx context.Context, cmd *cobra.Command, p *prompter, name string, pairs []string, confirm string) error {
	if _, err := client.FindHost(name); err != nil {
		return err
	}
	unlock, err := client.LockHost(ctx, name, "host set")
	if err != nil {
		return err
	}
	defer unlock()

	h, err := client.FindHost(name)
	if err != nil {
		return err
	}
	prev := cloneHost(h)
	parsed, err := parseHostSettings(h, pairs)
	if err != nil {
		return err
	}
	if err := validateHostSetConfirm(h.Name, pairs, parsed, confirm); err != nil {
		return err
	}
	if err := confirmRaiseTrust(ctx, p, h, parsed, confirm); err != nil {
		return err
	}
	if parsed.wantFloor != nil && *parsed.wantFloor > wall.None {
		if err := checkFloorSupported(ctx, h, *parsed.wantFloor); err != nil {
			return err
		}
	}

	oldFloor, oldIdle, oldTrusted, oldLabels := snapshotHostSettings(h)
	applyHostSettingsLocally(&h, parsed.items)
	if err := client.SaveHostIfUnchanged(ctx, &prev, h); err != nil {
		return err
	}
	if err := applyRemoteIdle(ctx, h, parsed); err != nil {
		return err
	}
	printHostSetChanges(cmd, h.Name, parsed.items, oldFloor, oldIdle, oldTrusted, oldLabels)
	return nil
}

func validateHostSetConfirm(hostName string, pairs []string, parsed parsedHostSettings, confirm string) error {
	if confirm == "" {
		return nil
	}
	if parsed.wantTrusted == nil || !*parsed.wantTrusted {
		return &client.UserError{
			Msg:  "--confirm applies only when setting trusted=true",
			Hint: brand.Name + " host set " + client.ShellQuote(hostName) + " " + strings.Join(pairs, " "),
			Code: "bad_value",
			Kind: client.KindUsage,
		}
	}
	if confirm != hostName {
		return &client.UserError{
			Msg:  fmt.Sprintf("--confirm %q does not match the host name %q", confirm, hostName),
			Hint: fmt.Sprintf("%s host set %s trusted=true --confirm %s", brand.Name, client.ShellQuote(hostName), client.ShellQuote(hostName)),
			Code: "bad_value",
			Kind: client.KindUsage,
		}
	}
	return nil
}

func confirmRaiseTrust(ctx context.Context, p *prompter, h client.Host, parsed parsedHostSettings, confirm string) error {
	if parsed.wantTrusted == nil || !*parsed.wantTrusted || h.Trusted {
		return nil
	}
	if err := verifyHostBeforeTrust(ctx, h, p.on); err != nil {
		return err
	}
	if confirm != "" {
		return nil
	}
	if !p.on {
		return p.needs(qHostSetTrust, "the typed host name to mark "+h.Name+" trusted", "--confirm", h.Name)
	}
	ok, err := p.typedName(fmt.Sprintf("Type %s to mark it trusted, or press Enter to keep it untrusted:", h.Name), h.Name)
	if err != nil {
		return err
	}
	if !ok {
		return p.cancel()
	}
	if stopped(ctx) {
		return errInterrupted
	}
	return nil
}

func snapshotHostSettings(h client.Host) (oldFloor, oldIdle string, oldTrusted bool, oldLabels map[string]string) {
	oldFloor = h.WallFloor
	if oldFloor == "" {
		oldFloor = api.LevelNone
	}
	if h.Machine != nil {
		oldIdle = h.Machine.IdleStop
	}
	return oldFloor, oldIdle, h.Trusted, maps.Clone(h.Labels)
}

func applyHostSettingsLocally(h *client.Host, items []hostSetting) {
	for _, s := range items {
		switch {
		case s.key == "floor":
			h.WallFloor = s.val
		case s.key == "idle":
			h.Machine.IdleStop = s.val
		case s.key == "trusted":
			h.Trusted = s.val == "true"
		case s.labelKey != "":
			if s.val == "" {
				delete(h.Labels, s.labelKey)
			} else {
				if h.Labels == nil {
					h.Labels = map[string]string{}
				}
				h.Labels[s.labelKey] = s.val
			}
		}
	}
	if len(h.Labels) == 0 {
		h.Labels = nil
	}
}

func applyRemoteIdle(ctx context.Context, h client.Host, parsed parsedHostSettings) error {
	if parsed.wantIdle == nil || h.Machine == nil || !powersOffWhenIdle(h.Machine.Provider) {
		return nil
	}
	err := applyIdle(ctx, h)
	if err == nil {
		return nil
	}
	applied := appliedBeforeRemote(parsed.items, true)
	if len(applied) > 0 {
		return &client.UserError{
			Msg:  fmt.Sprintf("could not apply idle on %s (%v); already saved: %s", h.Name, err, strings.Join(applied, ", ")),
			Hint: brand.Name + " host set " + client.ShellQuote(h.Name) + " idle=" + *parsed.wantIdle,
			Code: "apply_idle_failed",
		}
	}
	return &client.UserError{
		Msg:  fmt.Sprintf("saved idle=%s locally, but applying it on %s failed: %v", *parsed.wantIdle, h.Name, err),
		Hint: brand.Name + " host set " + client.ShellQuote(h.Name) + " idle=" + *parsed.wantIdle,
		Code: "apply_idle_failed",
	}
}

func cloneHost(h client.Host) client.Host {
	cp := h
	if h.Labels != nil {
		cp.Labels = maps.Clone(h.Labels)
	}
	if h.Secrets != nil {
		cp.Secrets = append([]string(nil), h.Secrets...)
	}
	if h.Provider != nil {
		p := *h.Provider
		cp.Provider = &p
	}
	if h.Machine != nil {
		m := *h.Machine
		cp.Machine = &m
	}
	return cp
}

func appliedBeforeRemote(items []hostSetting, hasRemoteIdle bool) []string {
	var out []string
	for _, s := range items {
		if s.key == "idle" && hasRemoteIdle {
			continue
		}
		out = append(out, s.key+"="+s.val)
	}
	return out
}

func printHostSetChanges(cmd *cobra.Command, hostName string, items []hostSetting, oldFloor, oldIdle string, oldTrusted bool, oldLabels map[string]string) {
	curFloor := oldFloor
	curIdle := oldIdle
	curTrusted := oldTrusted
	curLabels := maps.Clone(oldLabels)
	for _, s := range items {
		switch {
		case s.key == "floor":
			say(cmd, "%s: floor %s → %s.\n", hostName, curFloor, s.val)
			curFloor = s.val
		case s.key == "idle":
			if curIdle == "" {
				say(cmd, "%s: idle %s.\n", hostName, s.val)
			} else {
				say(cmd, "%s: idle %s → %s.\n", hostName, curIdle, s.val)
			}
			curIdle = s.val
		case s.key == "trusted":
			say(cmd, "%s: trusted %t → %s.\n", hostName, curTrusted, s.val)
			curTrusted = s.val == "true"
		case s.labelKey != "":
			oldVal, existed := curLabels[s.labelKey]
			switch {
			case s.val == "":
				say(cmd, "%s: removed %s.\n", hostName, s.key)
				delete(curLabels, s.labelKey)
			case existed:
				say(cmd, "%s: %s %s → %s.\n", hostName, s.key, oldVal, s.val)
				curLabels[s.labelKey] = s.val
			default:
				say(cmd, "%s: %s %s.\n", hostName, s.key, s.val)
				if curLabels == nil {
					curLabels = map[string]string{}
				}
				curLabels[s.labelKey] = s.val
			}
		}
	}
}

func verifyHostBeforeTrust(ctx context.Context, h client.Host, interactive bool) error {
	dctx, cancel := context.WithTimeout(client.WithoutInstall(ctx), 30*time.Second)
	defer cancel()
	conn, err := client.Dial(dctx, h, interactive)
	if err != nil {
		return err
	}
	conn.Close()
	return nil
}

func parseHostSettings(h client.Host, pairs []string) (parsedHostSettings, error) {
	var out parsedHostSettings
	out.items = make([]hostSetting, 0, len(pairs))
	for _, pair := range pairs {
		k, v, ok := strings.Cut(pair, "=")
		if !ok || k == "" {
			return parsedHostSettings{}, &client.UserError{
				Msg:  fmt.Sprintf("%q is not key=value", pair),
				Hint: brand.Name + " host set " + client.ShellQuote(h.Name) + " idle=2h",
				Code: "bad_value",
				Kind: client.KindUsage,
			}
		}
		switch {
		case k == "floor":
			lvl, perr := wall.ParseLevel(v)
			if perr != nil {
				return parsedHostSettings{}, &client.UserError{
					Msg:  fmt.Sprintf("floor: %v", perr),
					Hint: brand.Name + " host set " + client.ShellQuote(h.Name) + " floor=process",
					Code: "bad_value",
					Kind: client.KindUsage,
				}
			}
			out.wantFloor = &lvl
			out.items = append(out.items, hostSetting{key: k, val: lvl.String()})
		case k == "idle":
			if h.Machine == nil {
				return parsedHostSettings{}, &client.UserError{
					Msg:  h.Name + " was not made by `host create`",
					Hint: "Use a host made by " + brand.Name + " host create.",
					Code: "not_created",
					Kind: client.KindUsage,
				}
			}
			if _, _, perr := parseDurationFlag("idle", v, true, time.Minute); perr != nil || strings.TrimSpace(v) == "" {
				return parsedHostSettings{}, &client.UserError{
					Msg:  fmt.Sprintf("%q is not an idle time", v),
					Hint: "Use a duration of a minute or more, such as 30m or 2h, or never.",
					Code: "bad_value",
					Kind: client.KindUsage,
				}
			}
			val := v
			out.wantIdle = &val
			out.items = append(out.items, hostSetting{key: k, val: val})
		case k == "trusted":
			if v != "true" && v != "false" {
				return parsedHostSettings{}, &client.UserError{
					Msg:  fmt.Sprintf("trusted %q must be true or false", v),
					Hint: brand.Name + " host set " + client.ShellQuote(h.Name) + " trusted=true",
					Code: "bad_value",
					Kind: client.KindUsage,
				}
			}
			b := v == "true"
			out.wantTrusted = &b
			out.items = append(out.items, hostSetting{key: k, val: v})
		case strings.HasPrefix(k, "label."):
			lk := strings.TrimPrefix(k, "label.")
			if lk == "" || strings.ContainsAny(lk, " \t\r\n=") {
				return parsedHostSettings{}, &client.UserError{
					Msg:  fmt.Sprintf("%q is not a valid label key", k),
					Hint: brand.Name + " host set " + client.ShellQuote(h.Name) + " label.env=dev",
					Code: "bad_value",
					Kind: client.KindUsage,
				}
			}
			out.items = append(out.items, hostSetting{key: k, val: v, labelKey: lk})
		default:
			return parsedHostSettings{}, unknownHostSetKey(h.Name, k)
		}
	}
	return out, nil
}

func checkFloorSupported(ctx context.Context, h client.Host, want wall.Level) error {
	dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	probe, err := probeHostForFloor(dctx, h)
	if err != nil {
		return err
	}
	if probe.KVM.StaleGroup() && h.SSH != "" {
		stopSSHMaster(dctx, h)
		if fresh, ferr := probeHostForFloor(dctx, h); ferr == nil {
			probe = fresh
		} else if errors.Is(ferr, context.Canceled) || errors.Is(ferr, context.DeadlineExceeded) {
			return ferr
		}
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

func probeHostForFloor(ctx context.Context, h client.Host) (api.CapProbe, error) {
	conn, err := client.Dial(ctx, h, true)
	if err != nil {
		return api.CapProbe{}, err
	}
	defer conn.Close()
	return conn.CapProbe(ctx, "")
}

func unknownHostSetKey(hostName, key string) error {
	known := []named{
		{name: "floor", answers: []string{"floor"}},
		{name: "idle", answers: []string{"idle"}},
		{name: "trusted", answers: []string{"trusted"}},
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
