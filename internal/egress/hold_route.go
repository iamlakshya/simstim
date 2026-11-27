package egress

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"

	"github.com/iamlakshya/simstim/internal/brand"
)

type pendingNetworkRefusalKey struct{}
type pendingNetworkRefusal struct {
	decision Decision
	line     string
}

func (s *Server) awaitCarrierPolicy(ctx context.Context, conn net.Conn, hdr StreamHeader, method string) Decision {
	request := Request{Host: hdr.Host, Port: hdr.Port, Method: method}
	original := originalSecretDestination(ctx, hdr.Host)
	return s.awaitUnknown(ctx, request, original, hdr.Via, conn, func(engine *Engine, candidate Request) Decision {
		inspect, err := s.needsHTTPInspection(hdr)
		inspect = inspect || s.needsSecretInspection(hdr.Host, hdr.Port)
		if err != nil {
			return Decision{Rule: "class:unavailable"}
		}
		if s.decoyHost(hdr.Host, hdr.Port) {
			return Decision{Allow: true, Rule: "decoy:inspection"}
		}
		if inspect && engine.permitsInterceptProbe(hdr.Host, hdr.Port, true) {
			return Decision{Allow: true, Rule: "policy:inspection"}
		}
		if !candidate.classifierProbe && s.classifierServiceInspection(engine, hdr.Host, hdr.Port) {
			return Decision{Allow: true, Rule: "service:inspection"}
		}
		return engine.evaluate(candidate, nil, evalPrecheck)
	})
}

func (s *Server) networkRefusalLine(decision Decision, request Request, original string) string {
	host, _ := publicNetworkHost(s.scrubber, request.Host, original)
	switch decision.pendingRefusal {
	case heldRefusalWaiting:
		return fmt.Sprintf("%s: %s:%d waits for an answer in %s. Try: answer in the policy sheet, then run the command again", brand.Name, host, request.Port, s.sessionRef())
	case heldRefusalPressure:
		return brand.Name + ": " + errHoldPressure.Error()
	case heldRefusalStopped:
		return brand.Name + ": unknown network requests stopped after repeated refusals. Try: update this session's policy to reset the refusal count"
	default:
		if decision.unknownPressure {
			return brand.Name + ": " + unknownResourceLine()
		}
		return RefusalLine(s.sessionRef(), host, request.Port)
	}
}

func (s *Server) refuseCarrierPolicy(ctx context.Context, conn net.Conn, reader io.Reader, hdr StreamHeader, method string, decision Decision, connect bool) AuditEntry {
	request := Request{Host: hdr.Host, Port: hdr.Port, Method: method}
	line := s.networkRefusalLine(decision, request, originalSecretDestination(ctx, hdr.Host))
	entry := AuditEntry{Host: hdr.Host, Port: hdr.Port, Mode: "splice", Method: method, Status: http.StatusForbidden, Rule: decision.Rule, Decision: "deny"}
	if decision.pendingRefusal == heldRefusalCancelled {
		return s.formatNetworkAudit(entry, originalSecretDestination(ctx, hdr.Host))
	}
	if connect {
		entry.Down, _ = writeHTTPStatus(conn, http.StatusForbidden, "Forbidden", line)
		return s.formatNetworkAudit(entry, originalSecretDestination(ctx, hdr.Host))
	}
	if hdr.Port == 443 && decision.pendingRefusal != heldRefusalNone {
		// This context can only force a local403. It never authorises a
		// request or origin, even if a late policy answer arrives meanwhile.
		ctx = context.WithValue(ctx, pendingNetworkRefusalKey{}, pendingNetworkRefusal{decision: decision, line: line})
		return s.serveIntercept(ctx, conn, reader, hdr, false)
	}
	return s.formatNetworkAudit(entry, originalSecretDestination(ctx, hdr.Host))
}

// Preserve the original authority spelling for the same source redactor.
// Canonical case folding must not hide an exposed value from audit display.
func (s *Server) formatNetworkAudit(entry AuditEntry, original string) AuditEntry {
	entry.Host, _ = publicNetworkHost(s.scrubber, entry.Host, original)
	return s.formatAudit(entry)
}
