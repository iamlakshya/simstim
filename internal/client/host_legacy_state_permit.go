package client

import (
	"context"
	"sync"
	"sync/atomic"
)

type legacyStateWriteKind uint8

const (
	legacyNewCredentialRecord legacyStateWriteKind = iota + 1
	legacyResumeCredentialRecord
	legacyPasteRecord
	legacyNewNoneReceipt
)

// Main's canonical specific writer supplies its actual typed call arguments.
// The engine never parses ledger or sandbox JSON to reconstruct this intent.
type legacyStateWriteIntent struct {
	kind     legacyStateWriteKind
	conn     *Conn
	request  *GrantRequest
	session  string
	outbound Outbound
	none     *SandboxPolicy
	paste    *legacyPasteWriteProof
}

type legacyStateWriteKey struct{}

type legacyStateWritePermit struct {
	intent      legacyStateWriteIntent
	observation *HostStateObservation
	op          *heldLock
	source      hostStateSource
	target      string
	active      atomic.Bool
	consumed    atomic.Bool
}

func legacyStateWriteTarget(intent legacyStateWriteIntent) (string, error) {
	switch intent.kind {
	case legacyNewCredentialRecord, legacyResumeCredentialRecord:
		if intent.outbound.Kind != OutboundSecret || intent.none != nil || intent.paste != nil {
			return "", hostStateRemovalError("this legacy credential permit has another typed target")
		}
		return grantsFileName, nil
	case legacyPasteRecord:
		if intent.outbound.Kind != OutboundPaste || intent.outbound.Name != "" || intent.none != nil || intent.paste == nil {
			return "", hostStateRemovalError("this legacy paste permit has no actual Upload target")
		}
		return grantsFileName, nil
	case legacyNewNoneReceipt:
		if intent.none == nil || intent.paste != nil || intent.outbound.Kind != "" || intent.outbound.Name != "" {
			return "", hostStateRemovalError("this legacy None permit has another typed target")
		}
		return "sandboxes.json", nil
	default:
		return "", hostStateRemovalError("the legacy write intent has no supported canonical operation")
	}
}

// prepareLegacyStateWriteContext is called inside ONE canonical specific
// writer, after its final gates and before entering any byte callback. That
// writer defers revoke and never returns this derived context to its caller.
func prepareLegacyStateWriteContext(ctx context.Context, intent legacyStateWriteIntent) (context.Context, func(), error) {
	if ctx.Value(legacyStateWriteKey{}) != nil || ctx.Value(hostMembershipContextKey{}) != nil ||
		ctx.Value(hostMutationContextKey{}) != nil || ctx.Value(hostForgetMembershipKey{}) != nil {
		return ctx, nil, hostStateRemovalError("a legacy write cannot begin inside another write or local membership callback")
	}
	o, _ := ctx.Value(hostStateObservationKey{}).(*HostStateObservation)
	conn, err := o.legacyTransport(ctx)
	if err != nil {
		return ctx, nil, err
	}
	if intent.conn == nil || intent.conn != conn {
		return ctx, nil, hostStateRemovalError("the legacy intent belongs to another connection")
	}
	intent.none = cloneSandboxPolicy(intent.none)
	target, err := legacyStateWriteTarget(intent)
	if err != nil {
		return ctx, nil, err
	}
	if err := validateLegacyStateWriteIntent(ctx, &intent); err != nil {
		return ctx, nil, err
	}
	if err := withHostMembershipContext(ctx, o.checkMembership); err != nil {
		return ctx, nil, err
	}
	permit := &legacyStateWritePermit{intent: intent, observation: o, op: o.op, source: o.source, target: target}
	permit.active.Store(true)
	bound, cancel := context.WithCancel(ctx)
	bound = context.WithValue(bound, legacyStateWriteKey{}, permit)
	return bound, sync.OnceFunc(func() { permit.active.Store(false); cancel() }), nil
}

// consume runs before the lower engine takes membership or invokes a byte
// callback. The same one main-owned semantic validator may take request.mu
// and read canonical policy here; it must never be called from that callback.
func (p *legacyStateWritePermit) consume(ctx context.Context, o *HostStateObservation, subdirs []string, name string) error {
	if p == nil || !p.active.Load() || p.observation != o || p.op == nil || p.op != o.op || p.target != name ||
		!sameHostTransportSource(p.source, o.source) {
		return hostStateRemovalError("the legacy write permit ended or belongs to another exact observation")
	}
	if p.target == grantsFileName && len(subdirs) != 0 || p.target == "sandboxes.json" &&
		(len(subdirs) != 2 || subdirs[0] != "hosts" || subdirs[1] != o.key) {
		return hostStateRemovalError("the legacy write permit names another canonical file")
	}
	conn, err := o.legacyTransport(ctx)
	if err != nil {
		return err
	}
	if conn != p.intent.conn || !p.consumed.CompareAndSwap(false, true) {
		return hostStateRemovalError("this one-call legacy write permit has already been consumed")
	}
	if err := validateLegacyStateWriteIntent(ctx, &p.intent); err != nil {
		return err
	}
	if !p.active.Load() {
		return hostStateRemovalError("the canonical legacy writer returned before state admission")
	}
	return ctx.Err()
}
