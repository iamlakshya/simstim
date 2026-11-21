package client

import (
	"context"
	join "github.com/iamlakshya/simstim/internal/join/code"
	"maps"
	"slices"
	"time"
)

func cloneHostInviteV1(invite HostInvite) HostInvite {
	invite.Labels = maps.Clone(invite.Labels)
	return invite
}
func sameHostInviteV1(left, right HostInvite) bool {
	return left.Version == right.Version && left.Name == right.Name && left.IdentityFile == right.IdentityFile && left.PublicKey == right.PublicKey && left.Nonce == right.Nonce && left.Expires.Equal(right.Expires) && maps.Equal(left.Labels, right.Labels)
}

// Only the actual Begin producer owns the private original and invite lock.
// Exported presentation data never selects a replacement registration or route.
func (a *InviteAcceptance) checkOriginalInviteV1(ctx context.Context) error {
	if a == nil || a.unlock == nil || a.consumed || a.ctx == nil || ctx.Value(inviteAcceptanceKey{}) != a || !ValidHostName(a.original.Name) || !sameHostInviteV1(a.Invite, a.original) {
		return hostStateRemovalError("the original invitation or its presentation changed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := a.ctx.Err(); err != nil {
		return err
	}
	current, err := readHostInvite(a.original.Name)
	if err != nil {
		return err
	}
	if !sameHostInviteV1(current, a.original) {
		return inviteError("invite_unknown", "the original invite record changed", "Make a fresh invite on the laptop.")
	}
	if !time.Now().Before(a.original.Expires) {
		return inviteError("invite_expired", "the original invite expired", "Make a fresh invite on the laptop.")
	}
	return nil
}

func cloneJoinedReplyV1(reply join.Reply) join.Reply {
	reply.Addresses = slices.Clone(reply.Addresses)
	reply.Fingerprints = slices.Clone(reply.Fingerprints)
	reply.Hops = cloneJoinedHops(reply.Hops)
	return reply
}
func sameJoinedReplyV1(left, right join.Reply) bool {
	if left.Nonce != right.Nonce || left.Account != right.Account || left.Port != right.Port || left.SuggestedTier != right.SuggestedTier || !slices.Equal(left.Addresses, right.Addresses) || !slices.Equal(left.Fingerprints, right.Fingerprints) || len(left.Hops) != len(right.Hops) {
		return false
	}
	for index, hop := range left.Hops {
		original := right.Hops[index]
		if hop.Account != original.Account || hop.Port != original.Port || !slices.Equal(hop.Addresses, original.Addresses) || !slices.Equal(hop.Fingerprints, original.Fingerprints) {
			return false
		}
	}
	return true
}
