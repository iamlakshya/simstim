package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/onboard"
)

type hostAddOptions struct {
	yes              bool
	wantKey          string
	tier             client.Tier
	needTier         bool
	noSandbox        bool
	rawRootMode      string
	imagesMode       string
	delegateIO       bool
	separateAccounts bool
	wake             bool
}

var (
	connectHostStandard = connectHost
	connectHostPrepare  = connectHostForPreparation
)

func hasSavedHostTier(ctx context.Context, name string) (bool, error) {
	hosts, err := client.LoadHosts()
	if err != nil {
		return false, err
	}
	tiers, err := client.LoadTiersContext(ctx, hosts)
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(hosts, func(h client.Host) bool { return h.Name == name }) && !tiers.NeedsAnswer(name), nil
}

func missingTierWithYes(p *prompter, q *question, host string) error {
	if !p.on {
		return p.needs(q, "the tier of "+host+" (own, throwaway or shared)", "--tier", "<tier>")
	}
	return &client.UserError{
		Msg:  q.cmd + " --yes needs --tier (own, throwaway or shared) for " + host + ", because a host's tier has no default",
		Hint: p.withFlags("--tier", "<tier>"), Code: "needs_answer", Kind: client.KindNeedsAnswer,
	}
}

func validateNoSandboxFlags(cmd *cobra.Command, noSandbox bool) error {
	if !noSandbox {
		return nil
	}
	for _, flag := range []string{"images", "root-step", "delegate-io", "separate-accounts"} {
		if cmd.Flags().Changed(flag) {
			return &client.UserError{
				Msg:  "--no-sandbox cannot be combined with --" + flag,
				Hint: "Omit --no-sandbox to prepare sandboxes, or omit --" + flag + ".",
				Code: "flags_conflict",
				Kind: client.KindUsage,
			}
		}
	}
	return nil
}

func addHostPrepared(cmd *cobra.Command, p *prompter, h client.Host, opts hostAddOptions) error {
	if err := validateNoSandboxFlags(cmd, opts.noSandbox); err != nil {
		return err
	}
	if opts.noSandbox {
		return addHostWithTier(cmd.Context(), p, h, opts.yes, opts.wantKey, opts.tier, opts.needTier)
	}
	prepOpts, err := parseHostPrepareFlags(cmd, opts.rawRootMode, opts.imagesMode, opts.delegateIO, opts.separateAccounts, opts.yes, opts.wake)
	if err != nil {
		return err
	}
	baseCtx := cmd.Context()
	// Compare the entry read before connecting, so a removed host stays removed.
	was, err := client.SavedEntry(h.Name)
	if err != nil {
		return err
	}
	chooseTier, err := chooseAddedTier(baseCtx, p, h, opts.yes, opts.tier, opts.needTier)
	if err != nil {
		return err
	}
	pin := hostKeyOptions(h, opts.wantKey, opts.yes, p.on)
	pin.Wants = client.AcceptedInviteKeys(baseCtx)
	checks, target, err := connectHostPrepare(baseCtx, p.out, client.HostChange{
		Kind: "host add", Was: was, Now: &h, Pin: true, Tier: opts.tier,
	}, chooseTier, installConsent(baseCtx, p, h, opts.yes), p.changes(), pin)
	if err != nil {
		return handleHostAddConnectError(baseCtx, p, h, err)
	}
	fmt.Fprintln(p.out, "\nSandboxes:")
	summary, err := runHostPrepareTargetV1(cmd, p, baseCtx, target, prepOpts)
	if err != nil {
		if isCancel(err) || baseCtx.Err() != nil {
			return p.cancelOf(err)
		}
		return err
	}
	savedHost := target.Host()
	if summary.tested() {
		savedHost = summary.saved
	}
	if err := offerAddedHostAgents(baseCtx, p, savedHost, opts.yes, checks); err != nil {
		return err
	}
	if summary.tested() {
		fmt.Fprintf(p.out, "\n%s is ready (wall floor: %s). Start a session with `%s new %s --agent claude`, or run `%s`.\n",
			client.CleanLine(savedHost.Name), client.CleanLine(savedHost.WallFloor), brand.Name, shellWord(savedHost.Name), brand.Name)
		return nil
	}
	fmt.Fprintf(p.out, "\n%s is saved, and no sandbox wall is ready yet. Run `%s host prepare %s` once the host is ready for sandboxes.\n",
		client.CleanLine(savedHost.Name), brand.Name, shellWord(savedHost.Name))
	return nil
}

func addHostWithTier(ctx context.Context, p *prompter, h client.Host, yes bool, want string, tier client.Tier, needTier bool) error {
	// Compare the entry read before connecting, so a removed host stays removed.
	was, err := client.SavedEntry(h.Name)
	if err != nil {
		return err
	}
	chooseTier, err := chooseAddedTier(ctx, p, h, yes, tier, needTier)
	if err != nil {
		return err
	}
	pin := hostKeyOptions(h, want, yes, p.on)
	pin.Wants = client.AcceptedInviteKeys(ctx)
	checks, err := connectHostStandard(ctx, os.Stderr, client.HostChange{Kind: "host add", Was: was, Now: &h, Pin: true, Tier: tier}, chooseTier, installConsent(ctx, p, h, yes), p.changes(), pin)
	if err != nil {
		return handleHostAddConnectError(ctx, p, h, err)
	}
	if err := offerAddedHostAgents(ctx, p, h, yes, checks); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "\n%s is ready. Start a session with `%s new %s --agent claude`, or run `%s`.\n", h.Name, brand.Name, h.Name, brand.Name)
	return nil
}

func handleHostAddConnectError(ctx context.Context, p *prompter, h client.Host, err error) error {
	if isCancel(err) || ctx.Err() != nil {
		return p.cancelOf(err)
	}
	if errors.Is(err, client.ErrUnchecked) {
		return p.needs(qHostKey, "the fingerprint of "+h.Name+"'s host key, or --yes to pin the key that it shows", "--host-key", "SHA256:<fingerprint>")
	}
	return err
}

func chooseAddedTier(ctx context.Context, p *prompter, h client.Host, yes bool, tier client.Tier, needTier bool) (func() (client.Tier, error), error) {
	if tier.Valid() || !needTier {
		return nil, nil
	}
	hasTier, err := hasSavedHostTier(ctx, h.Name)
	if err != nil || hasTier {
		return nil, err
	}
	if yes {
		return nil, missingTierWithYes(p, qHostTier, h.Name)
	}
	return func() (client.Tier, error) { return p.askTier(h.Name) }, nil
}

func offerAddedHostAgents(ctx context.Context, p *prompter, h client.Host, yes bool, checks []api.AgentCheck) error {
	if checks != nil {
		fmt.Fprintln(os.Stderr, "\nAgents:")
		printAgentChecks(os.Stderr, h.Name, checks)
	}
	if yes || !p.asks(qAddAgents) {
		return nil
	}
	f := onboard.NewFlow(h.Name, checks, p.changes())
	f.NoLogin = true
	if !f.Missing() {
		return nil
	}
	g := newGuide(ctx, guideOpts{}, nil)
	g.in, g.out, g.p, g.tty, g.log = p.in, p.out, p, p.on, p.changes()
	hostAddGuide(g)
	err := g.agents(h, f)
	if err != nil && (isCancel(err) || errors.Is(err, context.Canceled) || ctx.Err() != nil) {
		// Keep the log of the saved host and any agents installed before cancellation.
		return p.cancelOf(err)
	}
	return err
}
