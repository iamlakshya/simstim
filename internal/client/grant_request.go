package client

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
)

// GrantRequest keeps one command's intent and terminal answers on the
// laptop. Reuse it only for retries of that same logical session start.
type GrantRequest struct {
	Untrusted           bool
	SSHAgent            bool
	purpose             grantPurpose
	operation           grantOperation
	operationUntrusted  bool
	newIntent           *newGrantIntent
	legacyResume        *legacyResumeGrantIntent
	resumeGroup         bool
	review              *reviewGrantIntent
	push                *pushGrantIntent
	forge               *localForgePlan
	root                *rootStepIntent
	rootExecution       *rootStepExecution
	rootAccounts        *rootAccountPreparation
	metadataMode        *agentModeResources
	newSecrets          *newSecretPlan
	sdkStart            *sdkNewStartPlanV1
	hostSelection       *HostSelectionV1
	selectedHost        string
	hostAttempt         *HostAttemptV1
	autoGather          *autoPlacementGatheringV1
	namedNew            *newNamedSSHKeyPlan
	namedResume         *resumeNamedSSHKeyPlan
	rewind              *RewindPreparation
	namedPolicy         *namedSSHKeyPolicyPlanV1
	namedPolicySource   *namedSSHPolicySourceV1
	trustedJudge        *trustedJudgePlanV1
	armSecrets          *armSecretsPlanV1
	freshChild          *freshChildPlan
	imageSave           *imageSaveOperation
	imageFetch          *imageFetchIntentV1
	policyReplace       *policyReplaceOperation
	networkHold         *networkHoldOperation
	purge               *purgeOperationV1
	purgeUndo           *purgeRootUndoExecutionV1
	runtimeUpgrade      *runtimeUpgradeIntentV1
	sandboxPrepare      *sandboxPreparePlanV1
	imagePolicy         *imagePolicyUseV1
	resumeImage         *resumeImagePolicyV1
	newInputs           *newInputPreparation
	preparationSettings *settingsOperation
	preparationClosed   bool
	ending              bool
	requestContext      context.Context
	preparedVM          *VMNeedPlanV1
	preparedVMIntent    string

	recordMu  sync.Mutex
	selection *grantSelectionSource
	mu        sync.Mutex
	id        string
	token     string
	confirmed map[grantScope]bool
	recorded  map[grantScope]bool
	wall      *wallGrantIntent
	join      *joinGrantIntent
	resume    *resumeGrantIntent
}

type grantScope struct {
	host, hostID string
	tier         Tier
	kind         OutboundKind
	name         string
	purpose      grantPurpose
	operation    grantOperation
}

type grantRequestKey struct{}

// NewGrantRequest prepares local intent for one start or resume. Neither
// the request nor its terminal answers belongs in a host RPC.
func NewGrantRequest(untrusted, sshAgent bool) *GrantRequest {
	return &GrantRequest{Untrusted: untrusted, SSHAgent: sshAgent}
}

// WithGrantRequest carries r through the questions and retries of one
// command. The host receives none of it.
func WithGrantRequest(ctx context.Context, r *GrantRequest) context.Context {
	if r == nil {
		r = NewGrantRequest(false, false)
	}
	bound := context.WithValue(ctx, grantRequestKey{}, r)
	r.mu.Lock()
	if r.requestContext == nil {
		r.requestContext = bound
	}
	r.mu.Unlock()
	return bound
}

func grantRequestOf(ctx context.Context) *GrantRequest {
	r, _ := ctx.Value(grantRequestKey{}).(*GrantRequest)
	return r
}

func ensureGrantRequest(ctx context.Context) (context.Context, *GrantRequest) {
	if r := grantRequestOf(ctx); r != nil {
		return ctx, r
	}
	r := NewGrantRequest(false, false)
	return WithGrantRequest(ctx, r), r
}

type scopedGrantConfirmation struct {
	*ConfirmError
	scope   grantScope
	request *GrantRequest
}

func (e *scopedGrantConfirmation) Unwrap() error { return e.ConfirmError }

// ConfirmGrant records a yes that the CLI or launcher just read from a
// terminal. The answer names the exact host identity, tier and grant in
// err. It is not a persistent approval or an unattended override.
func ConfirmGrant(ctx context.Context, err error) (context.Context, error) {
	if stopped := ctx.Err(); stopped != nil {
		return ctx, stopped
	}
	var q *scopedGrantConfirmation
	if !errors.As(err, &q) || q.request == nil {
		return ctx, &UserError{Msg: "the grant question no longer belongs to this request",
			Hint: "Run the command again in a terminal.", Code: "grant_confirmation", Kind: KindRefused}
	}
	if r := grantRequestOf(ctx); r != nil && r != q.request {
		return ctx, &UserError{Msg: "the grant question belongs to another request",
			Hint: "Run the command again in a terminal.", Code: "grant_confirmation", Kind: KindRefused}
	}
	q.request.mu.Lock()
	if err := q.request.lifetimeErrorLocked(); err != nil {
		q.request.mu.Unlock()
		return ctx, err
	}
	if q.scope.purpose != q.request.purpose {
		q.request.mu.Unlock()
		return ctx, grantPurposeError()
	}
	if q.scope.operation != q.request.operation {
		q.request.mu.Unlock()
		return ctx, grantOperationError()
	}
	if stopped := ctx.Err(); stopped != nil {
		q.request.mu.Unlock()
		return ctx, stopped
	}
	if q.request.confirmed == nil {
		q.request.confirmed = map[grantScope]bool{}
	}
	q.request.confirmed[q.scope] = true
	q.request.mu.Unlock()
	return WithGrantRequest(ctx, q.request), nil
}

func allowRequestGrant(ctx context.Context, h Host, hostID string, tier Tier, o Outbound, settings Settings) error {
	ctx, r := ensureGrantRequest(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := requirePurposeOutbound(ctx, r, o); err != nil {
		return err
	}
	if requestGrantPurpose(r) == grantRemote && IsSessionMachineV1(h) && (o.Kind == OutboundSecret || o.Kind == OutboundSSHKey) {
		if err := checkSessionMachineSecretTierV1(h, settings, o.Name); err != nil {
			return err
		}
		tier = min(tier, TierShared)
	}
	key := grantScope{host: h.Name, hostID: hostID, tier: tier, kind: o.Kind, name: o.Name, purpose: requestGrantPurpose(r), operation: requestGrantOperation(r)}
	r.mu.Lock()
	if err := r.lifetimeErrorLocked(); err != nil {
		r.mu.Unlock()
		return err
	}
	o.Confirmed = r.confirmed[key]
	r.mu.Unlock()
	if o.Kind == OutboundSilentUpgrade && sessionMachineInitialRuntimeV1(ctx, h, hostID, r) {
		o.Confirmed = true
	}
	err := Allow(h, tier, o, settings)
	var q *ConfirmError
	if errors.As(err, &q) {
		if key.purpose == grantLocalReview {
			copy := *q
			copy.Prompt = fmt.Sprintf("Read %s on this computer to review pulled files?", o.Name)
			if q.Err != nil {
				local := *q.Err
				local.Msg = fmt.Sprintf("reading %s on this computer to review pulled files needs a terminal answer", o.Name)
				local.Hint = "Run the pull command in an interactive terminal to review these local values."
				copy.Err = &local
			}
			q = &copy
		} else if key.purpose == grantLocalForge && o.Kind == OutboundSecret {
			copy := *q
			copy.Prompt = fmt.Sprintf("Read %s on this computer to check this repository's visibility?", o.Name)
			if q.Err != nil {
				local := *q.Err
				local.Msg = "reading this local forge credential needs its own terminal answer"
				local.Hint = "Run the exact push request in an interactive terminal."
				copy.Err = &local
			}
			q = &copy
		}
		return &scopedGrantConfirmation{ConfirmError: q, scope: key, request: r}
	}
	return err
}

func grantHostTier(ctx context.Context, h Host) (Tier, string, error) {
	if _, err := ResolveHostTier(ctx, h); err != nil {
		return TierUnknown, "", err
	}
	if err := ctx.Err(); err != nil {
		return TierUnknown, "", err
	}
	if !ValidHostName(h.Name) {
		return TierUnknown, "", badHostNameError(h.Name)
	}
	hosts, err := LoadHosts()
	if err != nil {
		return TierUnknown, "", err
	}
	saved := false
	for _, now := range hosts {
		if now.Name == h.Name && SameEntry(now, h) {
			saved = true
			break
		}
	}
	if !saved {
		return TierUnknown, "", grantIdentityError(h)
	}
	tiers, err := LoadTiersContext(ctx, hosts)
	if err != nil {
		return TierUnknown, "", err
	}
	entry, _ := tiers.Entry(h.Name)
	return tiers.Of(h.Name), entry.HostID, nil
}

// HostGrantTier reads this laptop's current tier and recorded host ID.
// It carries cancellation through migration and policy-file lock waits.
func HostGrantTier(ctx context.Context, h Host) (Tier, string, error) {
	return grantHostTier(ctx, h)
}

func grantIdentityError(h Host) error {
	return &UserError{Msg: fmt.Sprintf("the saved session grants on %s belong to another host identity", h.Name),
		Hint: brand.Name + " host test " + ShellWord(h.Name), Code: "host_identity", Kind: KindRefused}
}

func requestCeiling(ctx context.Context, hostTier Tier) Tier {
	if r := grantRequestOf(ctx); r != nil && r.Untrusted {
		return min(hostTier, TierThrowaway)
	}
	return TierOwn
}

func (r *GrantRequest) identity() (id, token string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.token == "" {
		var b [16]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", "", err
		}
		r.token = hex.EncodeToString(b[:])
	}
	if r.id == "" {
		if r.id, err = api.RandomSessionID(); err != nil {
			return "", "", err
		}
	}
	return r.id, r.token, nil
}

func (r *GrantRequest) retryID() {
	r.mu.Lock()
	r.id = ""
	r.mu.Unlock()
}

func recordRequestGrant(ctx context.Context, h Host, hostID string, tier Tier, o Outbound) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r := grantRequestOf(ctx)
	if err := requirePurposeRecord(ctx, r, o); err != nil {
		return err
	}
	if r == nil {
		return RecordGrantContext(ctx, h, hostID, o)
	}
	scopeName := o.Name
	if o.Kind == OutboundRepo {
		var err error
		scopeName, err = canonicalGrantName(o.Kind, o.Name)
		if err != nil {
			return err
		}
	}
	key := grantScope{host: h.Name, hostID: hostID, tier: tier, kind: o.Kind, name: scopeName, purpose: requestGrantPurpose(r), operation: requestGrantOperation(r)}
	// A tier change may require a new confirmation, but repeated sends
	// in one logical start still count as one recorded exposure.
	key.tier = TierUnknown
	r.recordMu.Lock()
	defer r.recordMu.Unlock()
	r.mu.Lock()
	alreadyRecorded := r.recorded[key]
	r.mu.Unlock()
	if alreadyRecorded {
		return nil
	}
	if err := RecordGrantContext(ctx, h, hostID, o); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.recorded == nil {
		r.recorded = map[grantScope]bool{}
	}
	r.recorded[key] = true
	return nil
}
