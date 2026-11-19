package egress

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
)

type serviceRequest struct {
	request   Request
	pathGrant bool
	boundMint bool
	pushRepo  string
	discovery bool
}

func (service *serviceRequest) allowed(engine *Engine, permit *Permit) bool {
	return service.allowedAtState(engine, permit, nil)
}

func (service *serviceRequest) allowedAtState(engine *Engine, permit *Permit, state *memberState) bool {
	if service.boundMint && state == nil {
		return false
	}
	if service.pushRepo != "" {
		mode, pushPermit := evalRevalidate, permit
		if service.discovery {
			mode, pushPermit = evalPrecheck, nil
		}
		if !engine.evaluatePush(service.pushRepo, pushPermit, mode, service.request.Now).Allow {
			return false
		}
	}
	return engine.admitServiceRequest(service.request, permit, service.requiresPath(state), evalRevalidate).Allow
}

// The predicate reads the SAME immutable registry and binding predicate. It
// does not infer a binding from the hostname, a class, a decoy or a credential
// header. Retired bindings still protect their old exact authority.
func (service *serviceRequest) requiresPath(state *memberState) bool {
	if service.pathGrant {
		return true
	}
	if !service.boundMint || state == nil || state.version != MemberRegistryV2 {
		return false
	}
	authority := net.JoinHostPort(service.request.Host, strconv.Itoa(service.request.Port))
	for name := range state.secrets {
		if state.hasSecretResource(name, authority) {
			return true
		}
	}
	for name := range state.retired {
		if state.hasSecretResource(name, authority) {
			return true
		}
	}
	return false
}

func (s *Server) prepareServiceHTTPRequest(scope *originRequest, request *http.Request) string {
	barriers, err := serviceBarriersFor(scope.request)
	if err != nil {
		return "unavailable"
	}
	if len(barriers) == 0 {
		return ""
	}
	service := &serviceRequest{request: scope.request}
	needBody := false
	for _, barrier := range barriers {
		if barrier.Kind != "grant" {
			needBody = true
		}
	}
	var body []byte
	if needBody {
		input := request.Body
		if input == nil {
			input = http.NoBody
		}
		body, err = readModelBody(input)
		if err != nil {
			return "json"
		}
		request.Body = newModelBody(body)
		request.ContentLength = int64(len(body))
		request.Header.Del("Content-Length")
	}
	for _, barrier := range barriers {
		switch barrier.Kind {
		case "grant":
			if barrier.BoundOnly {
				service.boundMint = true
			} else {
				service.pathGrant = true
			}
		case "visibility":
			fields, err := serviceJSONFields(body)
			if err != nil {
				return "json"
			}
			_, visibility := fields["visibility"]
			_, private := fields["private"]
			service.pathGrant = service.pathGrant || visibility || private
		default:
			members := s.currentMembers()
			if members == nil || !api.ValidSessionID(members.metadata.SessionID) {
				return "identity"
			}
			prefix := brand.Name + "/" + members.metadata.SessionID + "/"
			var needPath bool
			var err error
			if barrier.Kind == "graphql" {
				needPath, err = inspectGraphQLRefWrite(scope.ctx, scope.request, body, prefix)
			} else {
				needPath, err = inspectRESTRefWrite(scope.ctx, scope.request, barrier.Kind, body, prefix)
			}
			if err != nil {
				return "shape"
			}
			service.pathGrant = service.pathGrant || needPath
		}
	}

	if err := s.policyMu.LockContext(scope.ctx); err != nil {
		return "cancelled"
	}
	defer s.policyMu.Unlock()
	engine := s.currentEngine()
	service.pathGrant = service.requiresPath(s.currentMembers())
	if !scope.allowed(engine, s.dialer) || !engine.admitServiceRequest(service.request, scope.permit, service.pathGrant, evalReserve).Allow {
		return "path"
	}
	scope.mu.Lock()
	scope.reserved = true
	scope.service = service
	scope.mu.Unlock()
	return ""
}

// REST fields are decoded with the standard library, without materialising
// unselected nested objects. Case aliases of security fields cannot disagree
// with a provider decoder. The original validated bytes are forwarded.
func serviceJSONFields(body []byte) (map[string]json.RawMessage, error) {
	if !utf8.Valid(body) || !json.Valid(body) {
		return nil, errors.New("invalid service JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	open, err := decoder.Token()
	if err != nil || open != json.Delim('{') {
		return nil, errors.New("service request must be a JSON object")
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok {
			return nil, errors.New("invalid service JSON key")
		}
		key = strings.ToLower(key)
		if _, duplicate := fields[key]; duplicate {
			return nil, errors.New("ambiguous service JSON key")
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return nil, err
		}
		fields[key] = raw
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing service JSON")
	}
	return fields, nil
}
