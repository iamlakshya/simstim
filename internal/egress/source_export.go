package egress

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"strconv"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// A source export is the one management writer's physical completion owner.
// It stores no permission or target, and consumes no held/user-flow slot.
type sourceExport struct {
	source *memberState
	cancel context.CancelFunc
	done   chan struct{}
}

func (s *Server) retireSourceExportLocked(ctx context.Context, next *memberState) error {
	s.flows.mu.Lock()
	owner := s.flows.export
	s.flows.mu.Unlock()
	if owner == nil || owner.source == next {
		return nil
	}
	if ctx == nil || ctx.Err() != nil {
		return memberError("cancelled", "source publication was cancelled", "Retry the freshly approved operation.")
	}
	owner.cancel()
	select {
	case <-owner.done:
		return nil
	case <-ctx.Done():
		return s.secretJoinUncertain()
	}
}

func (s *Server) joinSourceExport() {
	s.flows.mu.Lock()
	owner := s.flows.export
	s.flows.mu.Unlock()
	if owner != nil {
		owner.cancel()
		<-owner.done
	}
}

// controlPolicyTurn never blocks on policy while it owns the writer. Source
// mutation may be joining a flow whose finaliser needs that same writer.
func (s *Server) controlPolicyTurn(ctx context.Context) error {
	for {
		if err := s.ctlMu.LockContext(ctx); err != nil {
			return err
		}
		if s.policyMu.TryLock() {
			return nil
		}
		s.ctlMu.Unlock()
		if err := s.policyMu.LockContext(ctx); err != nil {
			return err
		}
		s.policyMu.Unlock()
	}
}

func (s *Server) holdResolutionLocked(args api.NetworkHoldActionArgs) (api.NetworkHoldResolution, error) {
	state := s.currentMembers()
	if !api.ValidNetworkHoldPositiveAction(args) || state == nil || s.holds == nil || args.Scope == api.NetworkHoldOnce {
		return api.NetworkHoldResolution{}, memberError("bad_request", "invalid pending network resolution", "Refresh the original request and its current scope before answering.")
	}
	request, revision, expires, sensitive, ok := s.holds.lookup(args.Request, s.now())
	if !ok {
		return api.NetworkHoldResolution{}, memberError("network_request_ended", "pending network request changed or ended", "Refresh pending requests before answering.")
	}
	if request.uploadBudgetOnly {
		return api.NetworkHoldResolution{}, uploadBudgetScopeError()
	}
	if sensitive {
		return api.NetworkHoldResolution{}, memberError("network_sensitive_scope", "this target cannot be published as a policy rule", "Choose once or session in protected memory for a target that contains a known credential.")
	}
	if revision != args.Revision || !s.now().Before(expires) {
		return api.NetworkHoldResolution{}, memberError("network_request_ended", "pending network request changed or ended", "Refresh pending requests before answering.")
	}
	// Private and special literals cannot acquire address permission from any
	// numeric held answer, including an Own existing-policy maximum.
	if address, err := netip.ParseAddr(request.Host); err == nil {
		if _, allowed := s.dialer.CheckAddr(address, request.Port, nil); !allowed {
			return api.NetworkHoldResolution{}, memberError("network_address", "a held answer cannot create a private-address exception", "Use an independently approved numeric policy rule before retrying this address.")
		}
	}
	return api.NetworkHoldResolution{Version: api.NetworkHoldV1, Request: args.Request, Revision: revision, Scope: args.Scope, Target: net.JoinHostPort(request.Host, strconv.Itoa(request.Port)), Expires: expires, Class: api.NetworkHoldPublicTarget}, nil
}

func (s *Server) writeNetworkHoldResolution(parent context.Context, writer *proto.Writer, request proto.Request) error {
	var args api.NetworkHoldActionArgs
	failure := decodeControlArgs(request.Args, &args)
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
		return errors.New("egress resolution has no owned control stream")
	}
	var result api.NetworkHoldResolution
	if failure == nil {
		result, failure = s.holdResolutionLocked(args)
	}
	var owner *sourceExport
	if failure == nil {
		var expireCancel context.CancelFunc
		ctx, expireCancel = context.WithDeadline(ctx, result.Expires)
		defer expireCancel()
		owner = &sourceExport{source: s.currentMembers(), cancel: cancel, done: make(chan struct{})}
		s.flows.mu.Lock()
		if s.flows.closing || s.flows.export != nil {
			failure = memberError("network_request_ended", "pending network publication is unavailable", "Retry with the current session owner.")
		} else {
			s.flows.export = owner
		}
		s.flows.mu.Unlock()
		if failure != nil {
			owner = nil
		}
	}
	s.policyMu.Unlock()
	if owner != nil {
		defer func() {
			// Source mutation waits here while holding policyMu. Completion cannot
			// reacquire it, even after cancellation or a partial physical write.
			s.flows.mu.Lock()
			if s.flows.export == owner {
				s.flows.export = nil
			}
			close(owner.done)
			s.flows.mu.Unlock()
		}()
	}
	response := proto.Response{ID: request.ID, OK: failure == nil}
	if failure != nil {
		var wireError *proto.Error
		if !errors.As(failure, &wireError) {
			wireError = memberError("network_request_ended", "pending network resolution failed", "Refresh pending requests with matching helpers.")
		}
		response.Error = wireError
	} else {
		response.Result, _ = json.Marshal(result)
	}
	payload, err := json.Marshal(response)
	if err != nil || len(payload)+1 > proto.MaxFrame {
		return proto.ErrFrameTooBig
	}
	return s.writeControlPayloadOwned(ctx, writer, proto.TResponse, payload)
}
