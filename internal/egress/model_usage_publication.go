package egress

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/iamlakshya/simstim/internal/proto"
)

// Model labels use the same owned management export lifecycle as a hold
// target: current whole-label redaction happens at the physical writer turn.
// A later incompatible secret Source cancels and joins before replacement.
func (s *Server) writeModelUsageSnapshot(parent context.Context, writer *proto.Writer, request proto.Request) error {
	ctx, cancel := context.WithTimeout(parent, controlWriteTimeout)
	defer cancel()
	if err := s.controlPolicyTurn(ctx); err != nil {
		return err
	}
	defer s.ctlMu.Unlock()
	if writer == nil {
		writer = s.ctl
	}
	if writer == nil || writer != s.ctl || s.ctlStream == nil || s.sess == nil {
		s.policyMu.Unlock()
		return errors.New("model usage has no owned control stream")
	}
	var failure error
	if len(request.Args) > 0 {
		failure = decodeControlArgs(request.Args, &struct{}{})
	}
	state := s.currentMembers()
	if state == nil {
		failure = memberError("model_usage", "the live model meter is unavailable", "Wait for this session's owned egress initialisation.")
	}
	var owner *sourceExport
	response := proto.Response{ID: request.ID, OK: failure == nil}
	if failure == nil {
		snapshot := s.usage.snapshotWithSourceV1(s.scrubber.snapshot())
		response.Result, _ = json.Marshal(snapshot)
		owner = &sourceExport{source: state, cancel: cancel, done: make(chan struct{})}
		s.flows.mu.Lock()
		if s.flows.closing || s.flows.export != nil {
			failure = memberError("model_usage", "the live model meter changed or ended", "Refresh the current session before reading its usage.")
			owner = nil
		} else {
			s.flows.export = owner
		}
		s.flows.mu.Unlock()
	}
	s.policyMu.Unlock()
	if owner != nil {
		defer func() {
			s.flows.mu.Lock()
			if s.flows.export == owner {
				s.flows.export = nil
			}
			close(owner.done)
			s.flows.mu.Unlock()
		}()
	}
	if failure != nil {
		response.OK = false
		response.Result = nil
		var detail *proto.Error
		if !errors.As(failure, &detail) {
			detail = memberError("model_usage", "the live model usage request is invalid", "Use matching helpers and the current owned session.")
		}
		response.Error = detail
	}
	payload, err := json.Marshal(response)
	if err != nil || len(payload)+1 > proto.MaxFrame {
		return proto.ErrFrameTooBig
	}
	return s.writeControlPayloadOwned(ctx, writer, proto.TResponse, payload)
}
