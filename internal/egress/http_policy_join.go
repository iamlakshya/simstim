package egress

import "context"

// Publication captures these actual revoked owners after binding its engine
// to the original ledger, before cancellation can remove them. Admission
// shares policyMu, which is released before waiting on native completion.
func policyPublicationFlows(revoked []activeFlow, lockdown bool) []activeFlow {
	var owners []activeFlow
	for _, flow := range revoked {
		if flow.http || lockdown {
			owners = append(owners, flow)
		}
	}
	return owners
}

func awaitPolicyPublication(ctx context.Context, owners []activeFlow) error {
	for _, owner := range owners {
		if owner.cancel == nil || owner.done == nil {
			return memberError("policy_cleanup_incomplete", "policy was applied, but an affected request has no original completion owner", "Keep the session running and stop the affected request before retrying with matching helpers.")
		}
	}
	for _, owner := range owners {
		owner.cancel()
	}
	for _, owner := range owners {
		select {
		case <-owner.done:
		case <-ctx.Done():
			// The policy is already published. Retain those same actual
			// owners for cleanup; cancellation never forges their fences
			// or closes a permitted carrier to manufacture completion.
			return memberError("policy_cleanup_incomplete", "policy was applied, but revoked request cleanup has not finished", "Keep the session running, wait for the original connections to finish, then retry the policy change.")
		}
	}
	return nil
}
