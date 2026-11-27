package client

import (
	"context"
	"errors"
	"maps"
	"slices"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

// JoinSession starts one member in an existing wall. It imports neither
// new policy nor SSH agent authority. The original parent stays unchanged.
func (c *Conn) JoinSession(ctx context.Context, a api.JoinArgs) (*Session, error) {
	if err := CheckSessionName(a.Name); err != nil {
		return nil, err
	}
	a.Secrets = maps.Clone(a.Secrets)
	ctx, _ = ensureGrantRequest(ctx)
	intent, _, err := c.joinGrantIntent(ctx, a.Parent, secretMapNames(a.Secrets), true)
	if err != nil {
		return nil, err
	}
	if a.Snapshot != nil && !sameGrantSnapshot(a.Snapshot, intent.snapshot) {
		return nil, joinIntentError()
	}
	ctx, release, err := c.beginGrant(ctx, "join", false)
	if err != nil {
		return nil, err
	}
	defer release()
	// The snapshot RPC has already completed outside the publication lock.
	// Its protected host CAS rejects any intervening membership or run change.
	intent, tier, err := c.joinGrantIntent(ctx, a.Parent, secretMapNames(a.Secrets), true)
	if err != nil {
		return nil, err
	}
	a, err = c.reserveJoin(ctx, a, intent, tier)
	if err != nil {
		return nil, err
	}
	if err := recordSecretNames(ctx, c.Host, c.HostID(), tier, a.Secrets); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	a.Snapshot, a.Argv = cloneGrantSnapshot(intent.snapshot), slices.Clone(a.Argv)
	var child Session
	if err := c.callWithSent(ctx, "spawn", a, &child, release); err != nil {
		return nil, err
	}
	if err := checkSessionID(c.Host.Name, child.ID); err != nil {
		return nil, err
	}
	if child.ID != a.ID {
		return nil, &UserError{Msg: "the helper returned another member's ID",
			Hint: "List the host's sessions and check the original sandbox.", Code: "protocol", Kind: KindRefused}
	}
	return &child, nil
}

func (c *Conn) reserveJoin(ctx context.Context, a api.JoinArgs, intent *joinGrantIntent, tier Tier) (api.JoinArgs, error) {
	r := grantRequestOf(ctx)
	for range 16 {
		id, token, err := r.identity()
		if err != nil {
			return a, &UserError{Msg: "could not make a member ID", Hint: "Try the command again.", Code: "session_id"}
		}
		if a.ID != "" && a.ID != id {
			return a, joinIntentError()
		}
		if p := intent.creation; p != nil {
			child := cloneSandboxPolicy(p)
			clearSandboxReceiptMetadata(child)
			child.SessionID, child.SSHAgent = id, false
			child.Ceiling = min(tier, p.Ceiling)
			child.CreatedAt, child.request = time.Now().UTC(), token
			child.Secrets = secretMapNames(a.Secrets)
			child.lineage = &sandboxLineage{Version: 1, Parent: a.Parent, Root: localSandboxRoot(p), Allowed: localSandboxNames(p)}
			if child.lineage.Allowed == nil {
				child.lineage.Allowed = []string{}
			}
			if err := reserveSandbox(ctx, c.Host, *child); errors.Is(err, errSandboxIDTaken) {
				r.retryID()
				continue
			} else if err != nil {
				return a, err
			}
		}
		// An unknown parent deliberately creates no local authority record.
		a.ID = id
		return a, nil
	}
	return a, &UserError{Msg: "could not reserve a member ID", Hint: "Try the command again.", Code: "session_id"}
}
