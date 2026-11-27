package egress

import (
	"context"
	"net/netip"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// NetworkHoldsV1Feature identifies the display-only pending-network protocol.
const NetworkHoldsV1Feature = api.FeatureNetworkHolds

// Private egress control operations and notification for pending requests.
const (
	CtlNetworkPending  = "network.pending"
	CtlNetworkRefuse   = "network.refuse"
	CtlNetworkDescribe = "network.describe"
	CtlNetworkResolve  = "network.resolve"
	CtlNetworkAnswer   = "network.answer"
	EventNetworkHold   = "network_hold"
)

// PendingNetworkCapacity reuses the existing active-flow ceiling. Pressure
// refuses only a new question, never an existing hold or a network decision.
const PendingNetworkCapacity = maxActiveFlows

// NetworkHoldEvent is a coalescible display invalidation, not a grant or ID.
// The existing worker fetches a complete bounded current snapshot, so reordered
// old notifications cannot resurrect a question after a newer snapshot.
type NetworkHoldEvent struct {
	Version  int    `json:"v"`
	Revision uint64 `json:"revision"`
}

type networkHoldRefuseArgs struct {
	ID string `json:"id"`
}

type networkHoldDescribeArgs struct {
	Version int    `json:"v"`
	ID      string `json:"id"`
}

type heldRefusalReason uint8

const (
	heldRefusalNone heldRefusalReason = iota
	heldRefusalWaiting
	heldRefusalPressure
	heldRefusalStopped
	heldRefusalRefused
	heldRefusalCancelled
)

func (s *Server) pendingNetworkSnapshot() api.NetworkHoldSnapshot {
	if s.holds == nil {
		return api.NetworkHoldSnapshot{Version: api.NetworkHoldV1, Revision: 1}
	}
	s.holds.expire(s.now())
	refusals := s.holds.refusedCount(s.now())
	snapshot := s.holds.snapshot()
	unknown := s.currentEngine().unknown
	snapshot.Stopped = (unknown == api.UnknownAsk || unknown == api.UnknownClassify) && refusals >= 5
	return snapshot
}

func (s *Server) refuseNetworkHold(raw []byte) error {
	var args networkHoldRefuseArgs
	if err := decodeControlArgs(raw, &args); err != nil {
		return err
	}
	if !api.ValidNetworkHoldID(args.ID) {
		return memberError("bad_request", "invalid pending network request ID", "List the current pending network requests, then retry.")
	}
	if s.holds == nil {
		return memberError("network_request_ended", "pending network request has ended", "Retry against the current policy.")
	}
	s.policyMu.Lock()
	if s.classifier != nil {
		s.classifier.cancelHold(args.ID)
	}
	_, changed := s.holds.refuse(args.ID, s.now())
	s.policyMu.Unlock()
	if !changed {
		return memberError("network_request_ended", "pending network request has ended", "Retry against the current policy.")
	}
	s.wakeExpiry()
	return nil
}

func (s *Server) describeNetworkHold(raw []byte) (api.NetworkHoldDescriptor, error) {
	var args networkHoldDescribeArgs
	if err := decodeControlArgs(raw, &args); err != nil {
		return api.NetworkHoldDescriptor{}, err
	}
	if args.Version != api.NetworkHoldV1 || !api.ValidNetworkHoldID(args.ID) || s.holds == nil {
		return api.NetworkHoldDescriptor{}, memberError("bad_request", "invalid pending network descriptor query", "Use the current request's opaque ID with matching helpers.")
	}
	s.policyMu.Lock()
	defer s.policyMu.Unlock()
	target, revision, expires, _, ok := s.holds.lookup(args.ID, s.now())
	if !ok {
		return api.NetworkHoldDescriptor{}, memberError("network_request_ended", "pending network request has ended", "Refresh pending requests before describing one.")
	}
	class := api.NetworkHoldPublicTarget
	if address, err := netip.ParseAddr(target.Host); err == nil {
		if _, allowed := s.dialer.CheckAddr(address, target.Port, nil); !allowed {
			class = api.NetworkHoldPrivateTarget
		}
	}
	reason := ""
	if target.uploadBudgetOnly {
		reason = api.NetworkHoldUploadBudget
	}
	return api.NetworkHoldDescriptor{Version: api.NetworkHoldV1, Request: args.ID, Revision: revision, Class: class, Reason: reason, Expires: expires}, nil
}

func (s *Server) emitNetworkHoldRevision(ctx context.Context, revision uint64) error {
	return s.writeControlJSON(ctx, nil, proto.TEvent, CtlEvent{Event: EventNetworkHold, NetworkHold: &NetworkHoldEvent{Version: api.NetworkHoldV1, Revision: revision}})
}
