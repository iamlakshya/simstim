package egress

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/iamlakshya/simstim/internal/proto"
)

// This borrowed prefix never closes or drains the still-owned guest body.
// The existing request scope closes that body after its physical IO stops.
type decoyPrefixInput struct{ io.Reader }

func (*decoyPrefixInput) Close() error { return nil }

func decoyRequestFailure(status int, detail *proto.Error) *requestSecretFailure {
	return &requestSecretFailure{requestFailureDetail: detail, detail: detail, status: status}
}

func (s *Server) decoyRequestView(ctx context.Context) (*requestSecretSnapshot, error) {
	if err := s.policyMu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.policyMu.Unlock()
	view := s.requestSecretViewLocked()
	if view == nil || view.decoyView == nil {
		return nil, errRequestSecretStreamView
	}
	return view.decoyView, nil
}

// Decode and inspect only the fixed first body window before policy can
// reserve or the origin can see a header. The ONE existing stream matcher
// checks exact raw/base64 tripwires. Its emitted original bytes are replayed
// by the same clearable prefix owner used by the push command guard.
func (s *Server) prepareDecoyHTTPPrefix(scope *originRequest, request *http.Request, original requestSecretRawView, scheme string) *requestSecretFailure {
	if err := s.policyMu.LockContext(scope.ctx); err != nil {
		return decoyRequestFailure(http.StatusForbidden, memberError("cancelled", "decoy request inspection was cancelled", "Retry the approved request when ready."))
	}
	view := s.requestSecretViewLocked()
	if view == nil || view.decoyView == nil {
		s.policyMu.Unlock()
		return nil
	}
	failure := checkRequestSecretTripwires(view, request, original, scope.request, scheme)
	s.policyMu.Unlock()
	if failure != nil {
		return failure
	}
	if request.Body == nil || request.Body == http.NoBody {
		return nil
	}
	body, decoded, err := decodeHTTPBody(request.Body, request.Header)
	if err != nil {
		return decoyRequestFailure(http.StatusUnsupportedMediaType, memberError("http_encoding", "invalid or unsupported encoded request body", "Send a complete identity, gzip or deflate body."))
	}
	request.Body = body
	if decoded {
		request.Header.Del("Content-Length")
		request.ContentLength = -1
	}
	input := &decoyPrefixInput{Reader: io.LimitReader(body, decoyRequestBodyBytes)}
	scanner, err := newRequestSecretStreamReader(scope.ctx, input, s.decoyRequestView)
	if err != nil {
		return decoyRequestFailure(http.StatusForbidden, memberError("cancelled", "decoy request inspection is unavailable", "Retry the approved request after the session is ready."))
	}
	prefix, err := io.ReadAll(scanner)
	_ = scanner.Close()
	if err != nil {
		clear(prefix)
		var used *decoyStreamUse
		if errors.As(err, &used) {
			failure := requestSecretRefusal(http.StatusForbidden, "secret_misuse")
			failure.decoy = used.name
			return failure
		}
		return decoyRequestFailure(http.StatusBadRequest, memberError("http_body", "request body inspection did not finish", "Send one complete HTTP body before retrying."))
	}
	request.Body = newPushBody(prefix, body)
	return nil
}
