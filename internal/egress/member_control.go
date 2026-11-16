package egress

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

const EventMemberAdd = "member_add"

func decodeControlArgs(raw []byte, target any) error {
	if err := uniqueControlJSON(raw); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(target) != nil || dec.Decode(&struct{}{}) != io.EOF {
		return memberError("bad_request", "invalid or unsupported egress control arguments", "Update the supervisor and egress helper together, then retry.")
	}
	return nil
}

func (s *Server) handleCtlRequest(w *proto.Writer, req proto.Request) {
	var value any
	var err error
	switch req.Op {
	case CtlCapabilities:
		if len(req.Args) != 0 {
			err = decodeControlArgs(req.Args, &struct{}{})
		}
		if err == nil {
			value = CapabilitiesResult{Features: []string{MemberAddV1Feature, MemberAddV2Feature, api.FeatureBoundSecrets}}
		}
	case CtlInit:
		value, err = s.initialiseControl(req.Args)
	case CtlBoundInit:
		value, err = s.initialiseBoundControl(req.Args)
	case CtlPolicyUpdate:
		err = s.replaceControlPolicy(req.Args)
		if err == nil {
			value = struct{}{}
		}
	case CtlMemberAdd:
		value, err = s.addVersionedControlMember(req.Args)
	case CtlArm:
		value, err = s.armControl(req.Args)
	case CtlSecretSaved:
		err = s.acknowledgeMintedState(req.Args)
		if err == nil {
			value = struct{}{}
		}
	default:
		err = memberError("unknown_op", "unknown egress control operation", "Update the host helper, then resume the session.")
	}
	response := proto.Response{ID: req.ID, OK: err == nil}
	if err != nil {
		var pe *proto.Error
		if !errors.As(err, &pe) {
			pe = memberError("egress_ctl", "egress control operation failed", "Check the saved policy, then retry with matching helpers.")
		}
		response.Error = pe
	} else if value != nil {
		response.Result, _ = json.Marshal(value)
	}
	_ = s.writeControlJSON(context.Background(), w, proto.TResponse, response)
}

func (s *Server) initialiseControl(raw []byte) (InitResult, error) {
	s.policyMu.Lock()
	defer s.policyMu.Unlock()
	if s.isInited() {
		return InitResult{}, memberError("bad_request", "egress is already initialised", "Send a policy update instead.")
	}
	var args InitArgs
	if err := decodeControlArgs(raw, &args); err != nil {
		return InitResult{}, err
	}
	next, err := prepareInitialState(args)
	if err != nil {
		return InitResult{}, err
	}
	s.publishControlState(next, true)
	return InitResult{CACert: string(s.ca.publicPEM)}, nil
}

func (s *Server) replaceControlPolicy(raw []byte) error {
	s.policyMu.Lock()
	defer s.policyMu.Unlock()
	var args PolicyUpdateArgs
	if err := decodeControlArgs(raw, &args); err != nil {
		return err
	}
	state := s.currentMembers()
	if state == nil {
		return memberError("bad_request", "egress is not initialised", "Initialise egress before replacing its policy.")
	}
	// Decoys are a cold-start setting. Keep this process's applied flag
	// while the network/classifier policy changes through the real path.
	args.Policy = state.withAppliedDecoyPolicy(args.Policy)
	if state.version == MemberRegistryV2 {
		ctx, cancel := s.controlMutationContext()
		defer cancel()
		next, err := cloneMemberState(state)
		if err != nil {
			return err
		}
		next.metadata.Policy = args.Policy
		if args.SessionRef != "" {
			next.metadata.SessionRef = args.SessionRef
		}
		prepared, err := prepareBoundRegistry(ctx, next)
		if err != nil {
			return err
		}
		s.publishControlState(prepared, false)
		return nil
	}
	nextArgs, err := state.initialArgs()
	if err != nil {
		return err
	}
	nextArgs.Agent = s.agentName()
	nextArgs.Policy = args.Policy
	if args.SessionRef != "" {
		nextArgs.SessionRef = args.SessionRef
	}
	next, err := prepareInitialState(nextArgs)
	if err != nil {
		return err
	}
	s.publishControlState(next, false)
	return nil
}

func (s *Server) addControlMember(raw []byte) (MemberAddResult, error) {
	s.policyMu.Lock()
	defer s.policyMu.Unlock()
	var args MemberAddArgs
	if err := decodeControlArgs(raw, &args); err != nil {
		return MemberAddResult{}, err
	}
	state := s.currentMembers()
	if state == nil {
		return MemberAddResult{}, memberError("bad_request", "egress is not initialised", "Initialise egress before adding a member.")
	}
	nextArgs, env, names, err := state.memberCandidate(args)
	if err != nil {
		return MemberAddResult{}, err
	}
	next, err := prepareInitialState(nextArgs)
	if err != nil {
		return MemberAddResult{}, err
	}
	if state.memberChanges(args) {
		s.publishControlState(next, false)
	}
	// The operation is identified by trusted canonical kind and requested
	// names. No caller ID, raw arguments or secret value enters this event.
	s.emitLeaseEvent(CtlEvent{Event: EventMemberAdd, Member: &MemberAudit{Kind: args.Kind, Secrets: names}})
	return MemberAddResult{Version: MemberRegistryV1, Env: env}, nil
}

func (s *Server) currentMembers() *memberState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.members
}

// publishControlState runs under policyMu after all fallible preparation.
// Both snapshots are replaced while their readers are excluded. The stable
// scrubber pointer remains valid for every old flow and pending audit.
func (s *Server) publishControlState(next *preparedInitial, initial bool) {
	next.engine.now = s.now
	s.ledger.syncPolicy(next.engine)
	valid := next.engine.leaseKeys()
	s.scrubber.mu.Lock()
	s.mu.Lock()
	s.engine, s.members = next.engine, next.state
	s.scrubber.secrets = next.redaction
	if initial {
		s.synth = newSyntheticTable(MaxSyntheticNames)
		s.dnsMode = next.dnsMode
		s.agent = next.state.metadata.Agent
		s.inited = true
	}
	if next.state.metadata.SessionRef != "" {
		s.ref = next.state.metadata.SessionRef
	} else {
		s.ref = next.state.metadata.SessionID
	}
	for key := range s.expired {
		if _, exists := valid[key]; !exists {
			delete(s.expired, key)
		}
	}
	s.mu.Unlock()
	s.scrubber.mu.Unlock()
	if !initial && !next.secretOnly {
		s.flows.revalidate(next.engine, s.dialer)
	}
	s.wakeExpiry()
}
