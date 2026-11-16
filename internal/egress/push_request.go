package egress

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
)

func (s *Server) preparePushHTTPRequest(scope *originRequest, request *http.Request) string {
	route, found := gitPushHTTPRoute(scope.request)
	if !found {
		return ""
	}
	if !route.valid {
		return "push_route"
	}
	if route.discovery {
		if request.Body != nil && request.Body != http.NoBody {
			var probe [1]byte
			count, err := request.Body.Read(probe[:])
			if count != 0 || err != io.EOF {
				return "push_discovery_body"
			}
		}
	} else {
		members := s.currentMembers()
		if members == nil {
			return "push_identity"
		}
		id := members.metadata.SessionID
		if id == "" || strings.ContainsAny(id, "/\\") {
			return "push_identity"
		}
		prefix := "refs/heads/" + brand.Name + "/" + id + "/"
		wire, err := readReceivePackPrefix(request.Body, prefix)
		if err != nil {
			return "push_refs"
		}
		request.Body = newPushBody(wire, request.Body)
	}
	if err := s.policyMu.LockContext(scope.ctx); err != nil {
		return "push_cancelled"
	}
	defer s.policyMu.Unlock()
	engine := s.currentEngine()
	if !scope.allowed(engine, s.dialer) {
		return "push_policy"
	}
	permit, mode := scope.permit, evalReserve
	if !engine.admitPushRequest(scope.request, route, permit, mode, time.Time{}).Allow {
		return "push_grant"
	}
	scope.mu.Lock()
	scope.reserved = true
	scope.service = &serviceRequest{request: scope.request, pushRepo: route.repo, discovery: route.discovery}
	scope.mu.Unlock()
	return ""
}

// One locked transaction checks both authorities before mutation. The POST's
// push lease is acquired first, allowing the exact protocol-network fallback
// to reuse it. Discovery reads push availability and only reserves independent
// network authority. The existing origin dial commits the original permit.
func (e *Engine) admitPushRequest(request Request, route pushHTTPRoute, permit *Permit, mode evalMode, at time.Time) Decision {
	if !route.valid || e.ledger == nil {
		return Decision{Rule: "push:route"}
	}
	e.ledger.mu.Lock()
	defer e.ledger.mu.Unlock()
	if !e.validPermit(permit, mode) || mode != evalReserve {
		return Decision{Rule: "push:permit"}
	}
	if at.IsZero() {
		at = e.now()
	}
	request.Now = at
	if !e.evaluatePushLocked(route.repo, permit, evalPrecheck, at).Allow {
		return Decision{Rule: "push:ungranted"}
	}
	network := e.evaluateHTTPLocked(request, permit, evalPrecheck)
	if !network.Allow {
		return network
	}
	if !route.discovery {
		if !e.evaluatePushLocked(route.repo, permit, evalReserve, at).Allow {
			return Decision{Rule: "push:ungranted"}
		}
	}
	return e.evaluateHTTPLocked(request, permit, evalReserve)
}

type pushBody struct {
	reader io.Reader
	prefix *modelBody
	tail   io.ReadCloser
	once   sync.Once
	err    error
}

func newPushBody(prefix []byte, tail io.ReadCloser) *pushBody {
	owned := newModelBody(prefix)
	return &pushBody{reader: io.MultiReader(owned, tail), prefix: owned, tail: tail}
}

func (body *pushBody) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "<inspected request prefix redacted>")
}
func (body *pushBody) Read(output []byte) (int, error) { return body.reader.Read(output) }
func (body *pushBody) Close() error {
	body.once.Do(func() { body.err = body.tail.Close(); _ = body.prefix.Close() })
	return body.err
}
