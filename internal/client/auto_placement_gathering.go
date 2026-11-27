package client

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// autoPlacementGatheringV1 is a closed negative gate on the SAME fresh R.
// Its header is selection metadata. Only the unchanged strict H19 binder
// followed by markReadyV1 can open value selection and ordinary publication.
// Mutable fields are protected by request.mu.
type autoPlacementGatheringV1 struct {
	plan      *AutoPlacementPlanV1
	request   *GrantRequest
	candidate *autoPlacementCandidate
	selection *HostSelectionV1
	host      Host
	hostID    string
	tier      Tier
	source    hostStateSource
	wakeClass int
	wakeScope grantScope
	ready     bool
}

type autoPlacementBindingKeyV1 struct{}

func (p *HostAttemptPlanV1) bindAutomaticAttemptV1(ctx context.Context, request *GrantRequest, candidate *autoPlacementCandidate) (context.Context, error) {
	if p == nil || p.automatic == nil || request == nil || candidate == nil || ctx == nil {
		return ctx, grantOperationError()
	}
	if err := p.automatic.checkCandidateV1(ctx, candidate); err != nil {
		return ctx, err
	}
	if before := grantRequestOf(ctx); before != nil && before != request {
		return ctx, grantOperationError()
	}
	ctx = WithGrantRequest(ctx, request)
	// Establish the normal closed operation before installing its negative
	// gate. This creates no transport selection, answer or remote grant.
	if err := requireGrantOperation(ctx, grantOpNew); err != nil {
		return ctx, err
	}
	request.mu.Lock()
	defer request.mu.Unlock()
	if !freshHostSelectionRequestV1(request) || request.autoGather != nil || request.hostSelection != nil || request.selectedHost != "" {
		return ctx, grantOperationError()
	}
	gathering := &autoPlacementGatheringV1{plan: p.automatic, request: request, candidate: candidate,
		selection: p.selection, host: cloneObservationHost(candidate.host), hostID: candidate.selectionFactsV1().HostID,
		tier: candidate.tier, source: candidate.originalSource, wakeClass: candidate.wakeClass}
	if !ValidHostID(gathering.hostID) {
		return ctx, grantIdentityError(candidate.host)
	}
	gathering.wakeScope = grantScope{host: gathering.host.Name, hostID: gathering.hostID, tier: gathering.tier,
		kind: OutboundKind("placement-wake-v1"), name: gathering.source.generation + "/" + autoPlacementWakeNameV1(gathering.wakeClass),
		purpose: grantRemote, operation: grantOpNew}
	request.autoGather = gathering
	return ctx, nil
}

// H5 invokes this outside request.mu at requireRemoteGrantRequest. There is
// no caller boolean that opens the stage. The private transition key permits
// only strict local binding, and never relaxes the wire gate below.
func checkAutoNewReadyV1(ctx context.Context, request *GrantRequest) error {
	if ctx == nil || request == nil {
		return grantOperationError()
	}
	request.mu.Lock()
	gathering := request.autoGather
	if gathering == nil {
		request.mu.Unlock()
		return nil
	}
	valid := gathering.request == request && gathering.plan != nil && gathering.selection != nil &&
		gathering.candidate != nil && request.purpose == grantRemote && request.operation == grantOpNew &&
		request.operationUntrusted == request.Untrusted && !request.ending && !request.preparationClosed
	ready := gathering.ready
	bound := request.hostSelection == gathering.selection && request.selectedHost == gathering.host.Name
	request.mu.Unlock()
	if !valid || grantRequestOf(ctx) != request {
		return grantOperationError()
	}
	if ready && bound {
		return nil
	}
	if !ready && ctx.Value(autoPlacementBindingKeyV1{}) == gathering {
		return nil
	}
	return autoPlacementNotReadyV1()
}

// H5 invokes this at the actual writer before request preparation/write.
// Ready=false permits ONLY these exact metadata bodies. In particular the
// local binding key cannot turn this into a value or mutation capability.
func checkAutoNewWireV1(ctx context.Context, conn *Conn, request proto.Request, args any) error {
	if ctx == nil {
		return grantOperationError()
	}
	r := grantRequestOf(ctx)
	if r == nil {
		return nil
	}
	r.mu.Lock()
	gathering := r.autoGather
	if gathering == nil {
		r.mu.Unlock()
		return nil
	}
	ready := gathering.ready
	valid := gathering.request == r && r.purpose == grantRemote && r.operation == grantOpNew &&
		r.operationUntrusted == r.Untrusted && !r.ending && !r.preparationClosed
	bound := r.hostSelection == gathering.selection && r.selectedHost == gathering.host.Name
	r.mu.Unlock()
	if !valid || conn == nil || !SameEntry(conn.Host, gathering.host) || conn.hostTransport == nil ||
		conn.HostID() != gathering.hostID || !sameHostTransportSource(conn.hostTransport.source, gathering.source) {
		return grantOperationError()
	}
	if ready {
		if !bound {
			return grantOperationError()
		}
		return nil // Native writers retain their independent original checks.
	}
	switch request.Op {
	case "ls":
		if args == nil && (len(request.Args) == 0 || string(request.Args) == "null") {
			return nil
		}
	case "host.facts":
		var empty struct{}
		if api.ExactJSONFields(request.Args, empty) && json.Unmarshal(request.Args, &empty) == nil {
			return nil
		}
	case "sandbox.probe":
		var probe struct {
			ReleaseURL string `json:"release_url,omitempty"`
		}
		if api.ExactJSONFields(request.Args, probe) && json.Unmarshal(request.Args, &probe) == nil && probe.ReleaseURL == "" {
			return nil
		}
	}
	return autoPlacementNotReadyV1()
}

func (p *AutoPlacementPlanV1) bindReadyCandidateV1(ctx context.Context, request *GrantRequest, candidate *autoPlacementCandidate) (context.Context, error) {
	if ctx == nil || request == nil || candidate == nil || candidate.observation == nil || grantRequestOf(ctx) != request {
		return ctx, grantOperationError()
	}
	if err := p.checkCandidateV1(ctx, candidate); err != nil {
		return ctx, err
	}
	if err := candidate.observation.check(ctx); err != nil {
		return ctx, err
	}
	request.mu.Lock()
	gathering := request.autoGather
	valid := gathering != nil && gathering.plan == p && gathering.request == request && gathering.candidate == candidate &&
		!gathering.ready && gathering.selection == p.selection && SameEntry(gathering.host, candidate.host) &&
		gathering.hostID == candidate.observation.hostID && gathering.tier == candidate.tier &&
		sameHostTransportSource(gathering.source, candidate.observation.source)
	request.mu.Unlock()
	if !valid {
		return ctx, grantOperationError()
	}
	transition := context.WithValue(ctx, autoPlacementBindingKeyV1{}, gathering)
	if _, err := p.selection.bindSelectedAutoNewRequestV1(transition, request, candidate); err != nil {
		return ctx, err
	}
	// Keep the transition key local. Return the original same-R context only
	// after the exact object's ready transition and strict binding both hold.
	if err := p.checkCandidateV1(ctx, candidate); err != nil {
		return ctx, err
	}
	request.mu.Lock()
	defer request.mu.Unlock()
	if request.autoGather != gathering || gathering.ready || request.hostSelection != p.selection ||
		request.selectedHost != candidate.host.Name || request.preparationClosed || request.ending {
		return ctx, grantOperationError()
	}
	gathering.ready = true
	return ctx, nil
}

// Caller holds r.mu. H19 substitutes this for its old len(confirmed)==0
// clause. Only this SAME private stage's exact wake answer can precede bind.
func freshAutoPlacementWakeAnswersV1(r *GrantRequest) bool {
	if len(r.confirmed) == 0 {
		return true
	}
	gathering := r.autoGather
	if gathering == nil || gathering.request != r || gathering.plan == nil || gathering.selection == nil || gathering.candidate == nil ||
		gathering.ready || gathering.wakeClass != 2 || len(r.confirmed) != 1 || !ValidHostName(gathering.host.Name) ||
		!ValidHostID(gathering.hostID) || !gathering.tier.Valid() || !ValidHostID(gathering.source.generation) || !gathering.source.root.Known() {
		return false
	}
	want := grantScope{host: gathering.host.Name, hostID: gathering.hostID, tier: gathering.tier,
		kind: OutboundKind("placement-wake-v1"), name: gathering.source.generation + "/paid wake", purpose: grantRemote, operation: grantOpNew}
	if gathering.wakeScope != want || r.purpose != grantRemote || r.operation != grantOpNew {
		return false
	}
	return r.confirmed[gathering.wakeScope]
}

func (p *AutoPlacementPlanV1) paidWakeQuestionV1(ctx context.Context, candidate *autoPlacementCandidate) error {
	r := grantRequestOf(ctx)
	if r == nil {
		return grantOperationError()
	}
	r.mu.Lock()
	gathering := r.autoGather
	if gathering == nil || gathering.plan != p || gathering.request != r || gathering.candidate != candidate || gathering.ready || gathering.wakeClass != 2 {
		r.mu.Unlock()
		return grantOperationError()
	}
	scope := gathering.wakeScope
	answered := r.confirmed[scope]
	r.mu.Unlock()
	if answered {
		return nil
	}
	provider := "script"
	if candidate.host.Machine != nil {
		provider = CleanLine(candidate.host.Machine.Provider)
	}
	question := &ConfirmError{Prompt: "No awake host can take it. Wake " + candidate.host.Name + " (" + provider + ", billed while it runs)?",
		Kind: ConfirmYesNo, Host: candidate.host.Name, Grant: Outbound{Kind: scope.kind, Name: scope.name}, Err: &UserError{
			Msg: "the selected paid wake needs confirmation", Hint: "Answer in this request's terminal, or start with --allow-wake.", Code: "wake_confirmation", Kind: KindRefused}}
	return &scopedGrantConfirmation{ConfirmError: question, scope: scope, request: r}
}

func autoPlacementNotReadyV1() error {
	return &UserError{Msg: "automatic placement has not bound fresh accepted facts for this request",
		Hint: "Finish the same request's metadata gathering before preparing values or starting a session.", Code: "placement_not_ready", Kind: KindRefused}
}

// Preserve a genuine scoped question through the common owner without
// turning that UI question into evidence that this host attempt failed.
func autoPlacementQuestionV1(err error) bool {
	var question *scopedGrantConfirmation
	return errors.As(err, &question) && question.scope.kind == OutboundKind("placement-wake-v1")
}
