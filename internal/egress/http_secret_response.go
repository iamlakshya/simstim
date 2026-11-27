package egress

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/iamlakshya/simstim/internal/api"
)

type boundHTTPSecrets struct{ *boundHTTPSecretState }

type boundHTTPSecretState struct {
	s         *Server
	scope     *originRequest
	authority string
	use       secretRequestUse
	rules     *responseRuleSnapshot
	active    bool
	webSocket bool
	misuse    atomic.Bool
	decoy     atomic.Pointer[string]
}

func (s *Server) prepareBoundHTTPSecrets(scope *originRequest, req *http.Request, plan requestSecretPlan) (*boundHTTPSecrets, error) {
	if plan.generation == nil {
		return nil, nil
	}
	authority := net.JoinHostPort(scope.request.Host, strconv.Itoa(scope.request.Port))
	view := &boundHTTPSecrets{boundHTTPSecretState: &boundHTTPSecretState{s: s, scope: scope, authority: authority, use: secretRequestUseForPlan(plan),
		active: len(plan.generation.authorities[authority]) != 0 || s.needsSecretInspection(scope.request.Host, scope.request.Port)}}
	if !view.active {
		return view, nil
	}
	rules, err := pinnedResponseRuleSnapshot()
	if err != nil {
		return nil, err
	}
	// A judge-only binding may refuse known echoes but cannot select a
	// guest owner for newly minted credentials.
	if len(plan.generation.authorities[authority]) != 0 {
		view.rules = rules
	}
	view.webSocket, err = prepareBoundWebSocketRequest(req)
	return view, err
}

func (b *boundHTTPSecrets) reader(ctx context.Context, input io.ReadCloser) (io.ReadCloser, error) {
	return newResponseCredentialReader(ctx, input, func(ctx context.Context) (*responseEchoSnapshot, error) {
		return b.s.responseSecretSnapshot(ctx, b.authority, b.use)
	}, b.rules, func(ctx context.Context, value api.Secret, rule string) (api.Secret, error) {
		return b.s.registerResponseSecret(ctx, b.authority, b.use, value, rule)
	})
}

func (b *boundHTTPSecrets) scalar(value string) (string, error) {
	reader, err := b.reader(b.scope.ctx, io.NopCloser(strings.NewReader(value)))
	if err != nil {
		return "", err
	}
	defer reader.Close()
	result, err := io.ReadAll(io.LimitReader(reader, int64(len(value))+1))
	if err != nil || len(result) != len(value) {
		clear(result)
		return "", errResponseRegistry
	}
	safe := string(result)
	clear(result)
	return safe, nil
}

func (b *boundHTTPSecrets) headers(input http.Header, trailer bool) (http.Header, error) {
	output := make(http.Header, len(input))
	for name, values := range input {
		checked, err := b.scalar(name)
		if err != nil || checked != name {
			return nil, errResponseRegistry
		}
		for _, value := range values {
			safe, err := b.scalar(value)
			if err != nil {
				return nil, err
			}
			// Framing and upgrade controls must retain their protocol meaning.
			if safe != value && protectedSecretResponseField(name, trailer) {
				return nil, errResponseRegistry
			}
			output[name] = append(output[name], safe)
		}
	}
	return output, nil
}

func protectedSecretResponseField(name string, trailer bool) bool {
	if trailer {
		return !validHTTPPolicyTrailerName(name)
	}
	switch http.CanonicalHeaderKey(name) {
	case "Content-Length", "Transfer-Encoding", "Content-Encoding", "Connection", "Upgrade", "Trailer", "Sec-Websocket-Accept", "Sec-Websocket-Protocol", "Sec-Websocket-Extensions":
		return true
	}
	return false
}

func (s *Server) requestSecretStreamSnapshot(ctx context.Context) (*requestSecretSnapshot, error) {
	if err := s.policyMu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.policyMu.Unlock()
	view := s.requestSecretViewLocked()
	if view == nil {
		return nil, errors.New("request stand-in state is unavailable")
	}
	return view, nil
}
