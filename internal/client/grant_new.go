package client

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/iamlakshya/simstim/internal/agentbridge"
	"github.com/iamlakshya/simstim/internal/brand"
)

// CheckNewGrants checks the post-dial policy and helper capabilities before
// a front end fetches any values. Questions return to that front end.
func (c *Conn) CheckNewGrants(ctx context.Context, names []string, sshAgent bool) error {
	if err := checkAcceptedHostTransport(c); err != nil {
		return err
	}
	if err := c.requireSecretFeature(len(names) != 0); err != nil {
		return err
	}
	ctx, request := ensureGrantRequest(ctx)
	if _, _, err := c.newGrantTier(ctx, names, sshAgent); err != nil {
		return err
	}
	if sshAgent || request.SSHAgent {
		if err := CheckLocalSSHAgentContext(ctx); err != nil {
			return err
		}
	}
	_, err := SandboxPolicies(ctx, c.Host)
	return err
}

func (c *Conn) newGrantTier(ctx context.Context, names []string, sshAgent bool) (Tier, Tier, error) {
	if err := checkSessionMachineNewV1(ctx, c); err != nil {
		return TierUnknown, TierUnknown, err
	}
	ctx, r := ensureGrantRequest(ctx)
	if err := requireGrantOperation(ctx, grantOpNew); err != nil {
		return TierUnknown, TierUnknown, err
	}
	if err := checkRequestedGrantWall(ctx); err != nil {
		return TierUnknown, TierUnknown, err
	}
	if err := checkWallAgentIntent(ctx, sshAgent); err != nil {
		return TierUnknown, TierUnknown, err
	}
	if c.HostID() == "" {
		wall := requestWallIntent(ctx)
		if sshAgent || r.SSHAgent || wall != nil && wall.level != "none" {
			return TierUnknown, TierUnknown, grantIdentityError(c.Host)
		}
	}
	if err := bindRequestTransportSource(ctx, c); err != nil {
		return TierUnknown, TierUnknown, err
	}
	hostTier, hostID, err := grantHostTier(ctx, c.Host)
	if err != nil {
		return TierUnknown, TierUnknown, err
	}
	if hostID != "" && hostID != c.HostID() {
		return TierUnknown, TierUnknown, grantIdentityError(c.Host)
	}
	ceiling := requestCeiling(ctx, hostTier)
	tier := min(hostTier, ceiling)
	if err := bindNewGrantIntent(ctx, c.Host, c.HostID(), names, sshAgent || r.SSHAgent); err != nil {
		return tier, ceiling, err
	}
	settings, err := newOperationSettings(ctx)
	if err != nil {
		return tier, ceiling, err
	}
	if err := c.checkCreationCapability(ctx, tier, settings, names); err != nil {
		return tier, ceiling, err
	}
	sshAgent = sshAgent || r.SSHAgent
	if ceiling < TierOwn && !c.HasFeature("session.id") {
		return tier, ceiling, c.grantHelperOld("keep a request's tier")
	}
	if sshAgent {
		if err := allowRequestGrant(ctx, c.Host, c.HostID(), tier, Outbound{Kind: OutboundSSHAgent}, settings); err != nil {
			return tier, ceiling, err
		}
		if !c.HasFeature(agentbridge.Feature) || !c.HasFeature("session.id") {
			return tier, ceiling, c.grantHelperOld("forward an agent to one session")
		}
		if _, ok := c.route.t.(*SSH); !ok {
			return tier, ceiling, &UserError{Msg: "forwarding the laptop's SSH agent needs a host reached over SSH",
				Hint: "Choose a host reached over SSH.", Code: "needs_ssh"}
		}
		// Final publication rechecks metadata without making another
		// local health connection under the operation lock.
		if err := checkLocalSSHAgentPath(os.Getenv("SSH_AUTH_SOCK")); err != nil {
			return tier, ceiling, err
		}
	}
	if err := allowSecretNames(ctx, c.Host, c.HostID(), tier, settings, names); err != nil {
		return tier, ceiling, err
	}
	return tier, ceiling, nil
}

func (c *Conn) prepareNewGrants(ctx context.Context, a NewArgs) (NewArgs, Tier, error) {
	if err := checkWallGrantArgs(ctx, a.Wall, a.SSHAgent); err != nil {
		return a, TierUnknown, err
	}
	r := grantRequestOf(ctx)
	names := secretMapNames(a.Secrets)
	plan := newPlanOf(ctx)
	if plan != nil {
		names = plan.selection.Names()
	}
	tier, ceiling, err := c.newGrantTier(ctx, names, a.SSHAgent)
	if err != nil {
		return a, tier, err
	}
	a.SSHAgent = a.SSHAgent || r.SSHAgent
	if plan != nil {
		a, err = plan.final(ctx, c, a, tier, ceiling)
		return a, tier, err
	}
	// Even a normal start checks an existing file for damage, rather than
	// silently treating damaged grant state as a higher ceiling.
	if _, err := SandboxPolicies(ctx, c.Host); err != nil {
		return a, tier, err
	}
	if !c.HasFeature("session.id") {
		a.ID = ""
		return a, tier, nil
	}
	for range 16 {
		id, token, err := r.identity()
		if err != nil {
			return a, tier, &UserError{Msg: "could not make a session ID", Hint: "Try the command again.", Code: "session_id"}
		}
		creationWall, exposed := creationGrantFacts(ctx)
		sources, err := encodeRequestPolicySources(ctx, a)
		if err != nil {
			return a, tier, err
		}
		p := SandboxPolicy{PolicySources: sources, SessionID: id, HostID: c.HostID(), Ceiling: ceiling, SSHAgent: a.SSHAgent,
			Secrets: secretMapNames(a.Secrets), CreationWall: creationWall, Exposed: exposed, CreatedAt: time.Now().UTC(), request: token}
		err = reserveSandbox(ctx, c.Host, p)
		if errors.Is(err, errSandboxIDTaken) {
			r.retryID()
			continue
		}
		if err != nil {
			return a, tier, err
		}
		a.ID = id
		return a, tier, nil
	}
	return a, tier, &UserError{Msg: "could not reserve a session ID", Hint: "Try the command again.", Code: "session_id"}
}

func (c *Conn) grantHelperOld(what string) error {
	return &UserError{Msg: "the helper on " + c.Host.Name + " is too old to " + what,
		Hint: brand.Name + " host add " + ShellWord(c.Host.Name) + " (updates the helper)", Code: "helper_old", Kind: KindRefused}
}
