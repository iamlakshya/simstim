package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/onboard"
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
	checks, err := connectHost(ctx, os.Stderr, client.HostChange{Kind: "host add", Was: was, Now: &h, Pin: true, Tier: tier}, chooseTier, installConsent(ctx, p, h, yes), p.changes(), pin)
	if err != nil {
		if isCancel(err) || ctx.Err() != nil {
			return p.cancelOf(err)
		}
		if errors.Is(err, client.ErrUnchecked) {
			return p.needs(qHostKey, "the fingerprint of "+h.Name+"'s host key, or --yes to pin the key that it shows", "--host-key", "SHA256:<fingerprint>")
		}
		return err
	}
	if err := offerAddedHostAgents(ctx, p, h, yes, checks); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "\n%s is ready. Start a session with `%s new %s --agent claude`, or run `%s`.\n", h.Name, brand.Name, h.Name, brand.Name)
	return nil
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
