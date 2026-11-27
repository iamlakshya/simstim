package egress

import (
	"context"
	"net"
	"slices"
	"strconv"
	"sync"
)

type secretCarrierKey struct{}

type secretCarrier struct {
	tracker               *flowTracker
	ctx                   context.Context
	cancel                context.CancelFunc
	conn                  net.Conn
	header                StreamHeader // protected by tracker.mu
	inspected             bool         // protected by tracker.mu
	stopping              bool         // protected by tracker.mu
	done                  chan struct{}
	abortOnce, finishOnce sync.Once
}

// Main calls this before route selection/DNS/dial. finish is deferred BEFORE
// other ServeFlow finalisers, so it runs after actual IO/audit/permit cleanup.
// Existing admitted stream slots bound this bookkeeping. No slot is charged.
func (s *Server) beginSecretCarrier(ctx context.Context, conn net.Conn, hdr StreamHeader) (context.Context, func(), error) {
	if ctx == nil || conn == nil {
		return ctx, nil, memberError("bad_request", "flow has no owned carrier context", "Retry with matching helpers.")
	}
	owned, cancel := context.WithCancel(ctx)
	carrier := &secretCarrier{tracker: s.flows, ctx: owned, cancel: cancel, conn: conn, header: hdr, done: make(chan struct{})}
	s.flows.mu.Lock()
	closing := s.flows.closing
	if !closing {
		s.flows.carriers[carrier] = struct{}{}
	}
	s.flows.mu.Unlock()
	if closing {
		carrier.abort()
		return owned, nil, memberError("bad_request", "flow admission is closed", "Resume the sandbox before opening another flow.")
	}
	finish := func() {
		carrier.finishOnce.Do(func() {
			carrier.abort()
			carrier.tracker.mu.Lock()
			delete(carrier.tracker.carriers, carrier)
			carrier.tracker.mu.Unlock()
			close(carrier.done)
		})
	}
	return context.WithValue(owned, secretCarrierKey{}, carrier), finish, nil
}

func (carrier *secretCarrier) abort() {
	carrier.abortOnce.Do(func() {
		carrier.tracker.mu.Lock()
		carrier.stopping = true
		var cancel []func()
		for _, flow := range carrier.tracker.flows {
			if flow.carrier == carrier {
				cancel = append(cancel, flow.cancel)
			}
		}
		carrier.tracker.mu.Unlock()
		carrier.cancel()
		_ = carrier.conn.Close()
		for _, stop := range cancel {
			stop()
		}
	})
}

// The caller's physical mode decision and its publication are one registry
// transaction. needsHTTPInspection must not acquire policyMu recursively.
func (s *Server) secretCarrierInspection(ctx context.Context, hdr StreamHeader) (bool, error) {
	if err := s.policyMu.LockContext(ctx); err != nil {
		return false, err
	}
	defer s.policyMu.Unlock()
	carrier, ok := ctx.Value(secretCarrierKey{}).(*secretCarrier)
	if !ok || carrier.tracker != s.flows || carrier.ctx.Err() != nil {
		return false, memberError("bad_request", "flow has no active carrier admission", "Retry the request with matching helpers.")
	}
	inspect, err := s.needsHTTPInspection(hdr)
	secretInspection := s.needsSecretInspection(hdr.Host, hdr.Port)
	inspect = inspect || secretInspection
	if secretInspection && err == nil {
		_, spliceOnly, classErr := httpClass(hdr.Host)
		if classErr != nil {
			err = classErr
		} else if spliceOnly {
			err = memberError("unsupported", "secret tripwires require inspection that this destination cannot support", "Use a supported inspected HTTPS destination or a fresh session.")
		}
	}
	s.flows.mu.Lock()
	carrier.header = hdr
	if inspect && err == nil {
		carrier.inspected = true
	}
	s.flows.mu.Unlock()
	return inspect, err
}

// Plain HTTP is already physically inspected. Main calls this under the SAME
// gate as its request/binding admission, before body or origin IO.
func (s *Server) markSecretCarrierInspectedLocked(ctx context.Context, hdr StreamHeader) error {
	carrier, ok := ctx.Value(secretCarrierKey{}).(*secretCarrier)
	if !ok || carrier.tracker != s.flows || carrier.ctx.Err() != nil {
		return memberError("bad_request", "flow has no active inspected carrier", "Retry the request with matching helpers.")
	}
	s.flows.mu.Lock()
	carrier.header, carrier.inspected = hdr, true
	s.flows.mu.Unlock()
	return nil
}

// Main selects credentials and calls this while owning the same registry
// gate. Names are exact canonical registry dependencies, not another grant.
func (r *originRequest) tagSecretNamesLocked(names []string) error {
	state := r.s.currentMembers()
	if state == nil {
		return registryIntentError()
	}
	authority := net.JoinHostPort(r.request.Host, strconv.Itoa(r.request.Port))
	names = slices.Clone(names)
	slices.Sort(names)
	names = slices.Compact(names)
	for _, name := range names {
		if !state.hasSecretResource(name, authority) {
			return registryIntentError()
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.ctx.Err() != nil || r.flow == 0 {
		return memberError("cancelled", "request ended before credential admission", "Repeat the request after the approved operation finishes.")
	}
	r.s.flows.mu.Lock()
	defer r.s.flows.mu.Unlock()
	flow, present := r.s.flows.flows[r.flow]
	if !present {
		return memberError("cancelled", "request ownership ended before credential admission", "Repeat the request.")
	}
	flow.secretNames, flow.secretPending = names, false
	r.s.flows.flows[r.flow] = flow
	return nil
}

func (s *Server) closeSecretResources(ctx context.Context, names, pendingAuthorities, newBoundAuthorities []string) error {
	s.flows.mu.Lock()
	var owners []activeFlow
	carriers := make(map[*secretCarrier]struct{})
	missingCarrier := false
	for _, flow := range s.flows.flows {
		authority := net.JoinHostPort(flow.request.Host, strconv.Itoa(flow.request.Port))
		affected := flow.http && (flow.secretPending && slices.Contains(pendingAuthorities, authority) || slices.Contains(newBoundAuthorities, authority))
		for _, name := range flow.secretNames {
			affected = affected || slices.Contains(names, name)
		}
		if !flow.http && slices.Contains(newBoundAuthorities, authority) {
			affected = true
			if flow.carrier == nil {
				missingCarrier = true
			} else {
				carriers[flow.carrier] = struct{}{}
			}
		}
		if affected {
			owners = append(owners, flow)
		}
	}
	for carrier := range s.flows.carriers {
		authority := net.JoinHostPort(carrier.header.Host, strconv.Itoa(carrier.header.Port))
		if !carrier.inspected && slices.Contains(newBoundAuthorities, authority) {
			carriers[carrier] = struct{}{}
		}
	}
	s.flows.mu.Unlock()
	if missingCarrier {
		return memberError("secret_lifetime", "opaque flow lacks its physical carrier owner; replacement did not occur", "Stop and resume with matching lifetime support.")
	}
	for _, owner := range owners {
		if owner.cancel == nil || owner.http && owner.done == nil {
			return memberError("secret_lifetime", "request lacks its physical completion owner; replacement did not occur", "Stop and resume with matching helpers.")
		}
	}
	for carrier := range carriers {
		if carrier.done == nil || carrier.tracker != s.flows {
			return memberError("secret_lifetime", "carrier completion cannot be verified; replacement did not occur", "Stop and resume with matching helpers.")
		}
	}
	if err := ctx.Err(); err != nil {
		return memberError("cancelled", "replacement was cancelled before request closure", "Retry the freshly approved operation.")
	}
	for _, owner := range owners {
		owner.cancel()
	}
	for carrier := range carriers {
		carrier.abort()
	}
	for _, owner := range owners {
		if owner.http {
			select {
			case <-owner.done:
			case <-ctx.Done():
				return s.secretJoinUncertain()
			}
		}
	}
	for carrier := range carriers {
		select {
		case <-carrier.done:
		case <-ctx.Done():
			return s.secretJoinUncertain()
		}
	}
	return nil
}

func (s *Server) secretJoinUncertain() error {
	if s.sess != nil {
		_ = s.sess.Close()
	}
	return memberError("secret_lifetime_uncertain", "affected requests were closed but completion could not be confirmed; replacement did not occur", "The owned egress channel was closed. Stop and resume for a fresh approved generation.")
}
