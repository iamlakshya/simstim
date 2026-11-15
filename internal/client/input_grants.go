package client

import (
	"context"
	"os"
)

func (p *newInputPreparation) allowInputFacts(ctx context.Context, record bool, ceiling ...Tier) error {
	if p == nil || p.plan == nil || len(ceiling) > 1 {
		return grantOperationError()
	}
	settings, err := p.operation.Settings(ctx)
	if err != nil {
		return err
	}
	if len(p.plan.facts) == 0 {
		return p.operation.Revalidate(ctx)
	}
	if err := bindRequestTransportSource(ctx, p.conn); err != nil {
		return err
	}
	tier, hostID, err := grantHostTier(ctx, p.conn.Host)
	if err != nil {
		return err
	}
	if !ValidHostID(hostID) || hostID != p.conn.HostID() {
		return grantIdentityError(p.conn.Host)
	}
	tier = min(tier, requestCeiling(ctx, tier))
	if len(ceiling) != 0 {
		tier = min(tier, ceiling[0])
	}
	seen := map[string]inputSourceFact{}
	for _, fact := range p.plan.facts {
		if before, exists := seen[fact.localRoot]; exists {
			if before.kind != fact.kind || before.origin != fact.origin || !os.SameFile(before.identity, fact.identity) {
				return copyInputError("input_attribution", "a laptop input has conflicting original source facts")
			}
			continue
		}
		seen[fact.localRoot] = fact
		if err := ctx.Err(); err != nil {
			return err
		}
		outbound := Outbound{Kind: OutboundPaste}
		switch fact.kind {
		case inputSourceNonRepo:
		case inputSourceRepository:
			if fact.origin == "" {
				if tier == TierShared {
					return &UserError{Msg: "a laptop repository without a checked origin cannot be sent to this shared host",
						Hint: "Set its origin and list the repository in the host's repos setting.", Code: "repo_refused", Kind: KindRefused}
				}
			} else {
				name, err := NormalizeRepo(fact.origin)
				if err != nil {
					return err
				}
				outbound = Outbound{Kind: OutboundRepo, Name: name}
			}
		default:
			return copyInputError("input_attribution", "a laptop input has no original local source attribution")
		}
		if err := allowRequestGrant(ctx, p.conn.Host, hostID, tier, outbound, settings); err != nil {
			return err
		}
		if record {
			if err := recordRequestGrant(ctx, p.conn.Host, hostID, tier, outbound); err != nil {
				return err
			}
		}
	}
	return p.operation.Revalidate(ctx)
}

// Native metadata checks happen before the short first-write OP. They can
// never be moved under HostLock or replaced by remote source/origin claims.
func (p *newInputPreparation) revalidateInputFacts(ctx context.Context) error {
	if err := p.operation.Revalidate(ctx); err != nil {
		return err
	}
	seen := map[string]inputSourceFact{}
	for _, fact := range p.plan.facts {
		if before, exists := seen[fact.localRoot]; exists {
			if before.kind != fact.kind || before.origin != fact.origin || !os.SameFile(before.identity, fact.identity) {
				return copyInputError("input_attribution", "a laptop input has conflicting original source facts")
			}
			continue
		}
		seen[fact.localRoot] = fact
		if err := ctx.Err(); err != nil {
			return err
		}
		current, err := os.Stat(fact.localRoot)
		if err != nil || !os.SameFile(current, fact.identity) {
			return copyInputError("input_attribution", "a laptop input source changed after its local attribution")
		}
		if fact.kind == inputSourceRepository {
			current, err := repositorySourceProvenance(ctx, fact.localRoot, p.metadata)
			if err != nil {
				return err
			}
			if current.origin != fact.origin || !os.SameFile(current.identity, fact.identity) {
				return copyInputError("input_attribution", "a laptop repository origin changed after its local attribution")
			}
		} else if fact.kind == inputSourceNonRepo {
			marker, err := repositoryMarker(fact.localRoot)
			if err != nil {
				return err
			}
			if marker != "" {
				return copyInputError("input_attribution", "a laptop input acquired repository provenance after planning")
			}
		} else {
			return copyInputError("input_attribution", "a laptop input has no original local source attribution")
		}
	}
	return p.operation.Revalidate(ctx)
}
