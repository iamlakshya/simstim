package client

import (
	"context"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
)

type reviewGrantIntent struct {
	host              Host
	hostID, sessionID string
	sent, missing     []string
	creation          *SandboxPolicy
	receiptRevision   string
}

// NewReviewGrantRequest prepares local reads for one pulled-file review.
// It cannot authorise remote values or session changes. Keep the same
// request through terminal questions and retries of that review.
func NewReviewGrantRequest() *GrantRequest {
	return &GrantRequest{purpose: grantLocalReview}
}

// FetchReviewSecrets reads values only on this computer for a pulled-file
// review. Local creation facts limit the host's names. Names marked missing
// were not supplied and are excluded. No value or exposure record is sent.
func FetchReviewSecrets(ctx context.Context, original Host, id string, actuallySent, missing []string, terminal bool) (map[string]api.Secret, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r := grantRequestOf(ctx)
	if r == nil {
		r = NewReviewGrantRequest()
		ctx = WithGrantRequest(ctx, r)
	}
	if requestGrantPurpose(r) != grantLocalReview || r.Untrusted || r.SSHAgent || requestWallIntent(ctx) != nil {
		return nil, grantPurposeError()
	}
	sent, err := canonicalGrantNames(actuallySent)
	if err != nil {
		return nil, reviewAuthorityError()
	}
	notSent, err := canonicalGrantNames(missing)
	if err != nil {
		return nil, reviewAuthorityError()
	}
	for _, name := range notSent {
		if _, ok := slices.BinarySearch(sent, name); !ok {
			return nil, reviewAuthorityError()
		}
	}
	if err := bindRequestLocalSource(ctx, original); err != nil {
		return nil, err
	}
	_, hostID, err := grantHostTier(ctx, original)
	if err != nil {
		return nil, err
	}
	tier, creation, err := EffectiveSessionTier(ctx, original, id, hostID)
	if err != nil {
		return nil, err
	}
	if creation != nil && (creation.FutureLineage || creation.FutureDelivery || creation.futureDeliveryReceipt) {
		return nil, reviewAuthorityError()
	}
	if creation != nil && (creation.DeliveryIntent != nil || creation.deliveryReceipt != nil) {
		return fetchAcknowledgedReviewSecrets(ctx, original, id, sent, notSent, terminal)
	}
	names := make([]string, 0, len(sent))
	for _, name := range sent {
		if _, absent := slices.BinarySearch(notSent, name); absent {
			continue
		}
		if creation == nil || creation.FutureLineage || !slices.Contains(creation.Secrets, name) {
			return nil, reviewAuthorityError()
		}
		names = append(names, name)
	}
	ceiling, err := r.bindReviewIntent(original, hostID, id, sent, notSent, creation)
	if err != nil {
		return nil, err
	}
	tier = min(tier, ceiling)
	settings := LoadSettings()
	if err := allowResumeSecretNames(ctx, original, hostID, tier, settings, names, creation); err != nil {
		return nil, err
	}
	return FetchSecrets(ctx, settings, names, terminal)
}

func (r *GrantRequest) bindReviewIntent(h Host, hostID, id string, sent, missing []string, creation *SandboxPolicy) (Tier, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.purpose != grantLocalReview || r.operation != grantOpUnset || r.newIntent != nil || r.join != nil ||
		r.resume != nil || r.legacyResume != nil || r.resumeGroup {
		return TierUnknown, grantPurposeError()
	}
	if before := r.review; before != nil {
		if !SameEntry(before.host, h) || before.hostID != hostID || before.sessionID != id ||
			!slices.Equal(before.sent, sent) || !slices.Equal(before.missing, missing) ||
			!sameSandboxCreation(before.creation, creation) {
			return TierUnknown, reviewAuthorityError()
		}
	} else {
		r.review = &reviewGrantIntent{host: cloneGrantHost(h), hostID: hostID, sessionID: id,
			sent: slices.Clone(sent), missing: slices.Clone(missing), creation: cloneSandboxPolicy(creation)}
	}
	if p := r.review.creation; p != nil {
		return p.Ceiling, nil
	}
	return TierOwn, nil
}

func reviewAuthorityError() error {
	return &UserError{Msg: "this laptop cannot authorise a complete local secret review for this session",
		Hint: "Keep the pulled work in quarantine. Review it where the original session creation and supplied secret names are known.",
		Code: "review_authority", Kind: KindRefused}
}
