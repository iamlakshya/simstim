package client

import (
	"context"
	"fmt"
	"io"
	"reflect"

	"github.com/iamlakshya/simstim/internal/api"
)

// AutoPlacementOptionsV1 is original invocation metadata. AllowWake bypasses
// only the selected paid-wake question, never source or New admission checks.
type AutoPlacementOptionsV1 struct {
	AllowWake bool
	Progress  io.Writer
}

func restrictAutomaticAttemptDialV1(ctx context.Context) context.Context {
	return WithoutPolicyReplay(WithoutInstall(WithoutWake(ctx)))
}

func (p *AutoPlacementPlanV1) authoriseWakeV1(ctx context.Context, candidate *autoPlacementCandidate, _ AutoPlacementOptionsV1) (context.Context, error) {
	if p == nil || ctx == nil || candidate == nil {
		return ctx, grantOperationError()
	}
	options := p.wake // immutable invocation intent, never a later answer or candidate row
	if err := p.selection.Check(ctx); err != nil {
		return ctx, err
	}
	if err := p.checkCandidateV1(ctx, candidate); err != nil {
		return ctx, err
	}
	readOnly := func() context.Context { return restrictAutomaticAttemptDialV1(ctx) }
	if candidate.wakeClass == 0 {
		return readOnly(), nil
	}
	if !wakeAllowed(ctx) || p.settings.Wake == "never" {
		return ctx, autoPlacementWakeRefusedV1()
	}
	if candidate.wakeClass == 2 && p.settings.Wake != "always" && !options.AllowWake {
		if err := p.paidWakeQuestionV1(ctx, candidate); err != nil {
			return ctx, err
		}
	}
	if err := p.checkCandidateV1(ctx, candidate); err != nil {
		return ctx, err
	}
	if options.Progress != nil {
		if _, err := fmt.Fprintf(options.Progress, "Waking %s (%s).\n", candidate.host.Name, autoPlacementWakeNameV1(candidate.wakeClass)); err != nil {
			return ctx, err
		}
	}
	// The deliberate wake is source-bound separately. Metadata-only Dial
	// remains WithoutWake, including after a terminal said yes.
	owned, release, actual, err := beginHostTransportObservation(ctx, candidate.host)
	if err != nil {
		return ctx, err
	}
	defer release()
	if actual.kind != hostTransportSaved || actual.observation == nil ||
		!sameHostTransportSource(actual.observation.source, candidate.originalSource) {
		return ctx, grantIdentityError(candidate.host)
	}
	if candidate.host.Machine != nil {
		machine, err := OpenMachine(candidate.host)
		if err != nil {
			return ctx, err
		}
		err = ensureRunning(owned, candidate.host, machine)
		if err != nil {
			return ctx, err
		}
	} else {
		if err := checkHostTransportWake(owned, actual, candidate.host); err != nil {
			return ctx, err
		}
		if err := wake(owned, candidate.host); err != nil {
			return ctx, err
		}
	}
	if err := checkHostTransportAttempt(owned, actual, candidate.host); err != nil {
		return ctx, err
	}
	return readOnly(), nil
}

// These methods compose with the common HostAttempt owner. They allocate no
// separate request, process owner or generic retry callback.
func (p *HostAttemptPlanV1) nextAutomaticV1(ctx context.Context, interactive bool) (*HostAttemptV1, error) {
	p.mu.Lock()
	if err := p.admitNextLockedV1(ctx); err != nil {
		p.mu.Unlock()
		return nil, err
	}
	if p.automatic == nil || p.autoAttempts >= 3 {
		p.mu.Unlock()
		return nil, autoPlacementNoHostV1()
	}
	names := p.automatic.Names()
	if p.next >= len(names) {
		p.mu.Unlock()
		return nil, autoPlacementNoHostV1()
	}
	name := names[p.next]
	p.mu.Unlock()
	candidate, err := p.automatic.candidateV1(ctx, name)
	if err != nil {
		return nil, err
	}
	// newAttemptForTargetV1 must increment next/autoAttempts atomically when
	// it publishes p.active, as specified in the accompanying exact hook.
	return p.newAttemptForTargetV1(ctx, name, candidate, interactive)
}

func (p *HostAttemptPlanV1) refreshAutomaticAttemptV1(ctx context.Context, attempt *HostAttemptV1) error {
	if p == nil || attempt == nil || ctx == nil || p.automatic == nil {
		return grantOperationError()
	}
	p.mu.Lock()
	valid := p.active == attempt && !p.closed && attempt.plan == p
	p.mu.Unlock()
	if !valid {
		return grantOperationError()
	}
	attempt.mu.Lock()
	candidate, conn, request, original := attempt.candidate, attempt.conn, attempt.request, attempt.ctx
	attempt.mu.Unlock()
	if candidate == nil || conn == nil || request == nil || original == nil || !SameEntry(conn.Host, candidate.host) {
		return grantOperationError()
	}
	if err := p.automatic.refreshCandidateV1(ctx, conn, candidate); err != nil {
		return err
	}
	// No cache row can reach this binder: refresh just installed an actual
	// accepted-connection observation. The binder itself remains unchanged.
	bound, err := p.automatic.bindReadyCandidateV1(original, request, candidate)
	if err != nil {
		return err
	}
	attempt.mu.Lock()
	attempt.ctx = bound
	attempt.mu.Unlock()
	record, err := p.automatic.reserveV1(ctx, candidate)
	attempt.mu.Lock()
	// reservePlacement returns its exact owner even on an uncertain write.
	// The common owner must retain it and stop if err is non-nil.
	attempt.placement = record
	attempt.mu.Unlock()
	return err
}

func (p *AutoPlacementPlanV1) refreshCandidateV1(ctx context.Context, conn *Conn, candidate *autoPlacementCandidate) error {
	if p == nil || candidate == nil || conn == nil {
		return grantOperationError()
	}
	if err := p.checkCandidateV1(ctx, candidate); err != nil {
		return err
	}
	if err := checkFactsConnection(ctx, conn); err != nil {
		return err
	}
	observed, fence, err := p.store.readFencedHostFactsV1(ctx, conn)
	if err != nil {
		return err
	}
	if !SameEntry(observed.host, candidate.host) || !sameHostTransportSource(observed.source, candidate.originalSource) {
		return grantIdentityError(candidate.host)
	}
	// Checked source continuity is required before replacing advisory data;
	// the saved cache is never promoted into this actual observation type.
	candidate.observation, candidate.placementFence, candidate.cached, candidate.stopped = observed, fence, nil, false
	inflight, _, err := p.selectionHistoryV1(ctx, []*autoPlacementCandidate{candidate})
	if err != nil {
		return err
	}
	reason, err := p.filterCandidateV1(ctx, candidate, inflight[candidate.host.Name])
	if err != nil {
		return err
	}
	if reason != "" {
		return &refreshedPlacementRefusalV1{plan: p, candidate: candidate, observation: observed,
			actual: &UserError{Msg: "the selected host no longer passes the live placement filter: " + reason,
				Hint: "Use the next eligible host after this original attempt has closed.", Code: "no_host", Kind: KindRefused}}
	}
	if err := p.store.rememberFacts(ctx, []*hostFactsObservation{observed}, p.hosts); err != nil {
		return err
	}
	return p.selection.Check(ctx)
}

func (p *AutoPlacementPlanV1) reserveV1(ctx context.Context, candidate *autoPlacementCandidate) (*ownedPlacementRecord, error) {
	if p == nil || candidate == nil || candidate.observation == nil {
		return nil, grantOperationError()
	}
	if err := p.checkCandidateV1(ctx, candidate); err != nil {
		return nil, err
	}
	return p.store.reservePlacement(ctx, candidate, candidate.reason)
}

// placementArgumentsV1 runs before the actual NewSession call. Public caller
// metadata cannot replace the concrete selected candidate or winning reason.
func (p *AutoPlacementPlanV1) placementArgumentsV1(ctx context.Context, attempt *HostAttemptV1, args NewArgs) (NewArgs, error) {
	if p == nil || attempt == nil || attempt.candidate == nil || attempt.placement == nil {
		return args, grantOperationError()
	}
	candidate := attempt.candidate
	if err := bindPreparedPlacementMemberV1(ctx, attempt); err != nil {
		return args, err
	}
	if err := p.checkCandidateV1(ctx, candidate); err != nil {
		return args, err
	}
	want := &api.PlacementV1{Version: api.PlacementVersion, PlacedBy: "auto", Reason: candidate.reason}
	if args.Placement != nil && !reflect.DeepEqual(args.Placement, want) {
		return args, grantOperationError()
	}
	args.Placement = want
	return args, want.Validate()
}

func autoPlacementWakeRefusedV1() error {
	return &UserError{Msg: "automatic placement did not wake the selected host",
		Hint: "Choose an awake host, change placement.wake, or allow this paid wake explicitly.", Code: "wake_refused", Kind: KindRefused}
}

func autoPlacementWakeQuestionV1() error {
	return &UserError{Msg: "the selected paid wake needs an answer in this command's terminal",
		Hint: "Run new auto with input enabled to answer, or pass --allow-wake for this invocation.", Code: "wake_confirmation", Kind: KindRefused}
}

// Only the actual accepted facts refresh produces this local pre-New result.
// It proves no remote absence by itself. The common owner still joins every
// native contact and the original request before advancing to another host.
type refreshedPlacementRefusalV1 struct {
	plan        *AutoPlacementPlanV1
	candidate   *autoPlacementCandidate
	observation *hostFactsObservation
	actual      error
}

func (e *refreshedPlacementRefusalV1) Error() string { return e.actual.Error() }
func (e *refreshedPlacementRefusalV1) Unwrap() error { return e.actual }
