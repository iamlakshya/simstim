package egress

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"
)

type serviceRequest struct {
	request   Request
	pathGrant bool
}

func (service *serviceRequest) allowed(engine *Engine, permit *Permit) bool {
	decision := engine.evaluateHTTP(service.request, permit, evalRevalidate)
	return decision.Allow && (!service.pathGrant || explicitPathAllowance(decision))
}

func (s *Server) prepareServiceHTTPRequest(scope *originRequest, request *http.Request) string {
	barrier, err := serviceBarrierFor(scope.request)
	if err != nil {
		return "unavailable"
	}
	if barrier == nil {
		return ""
	}
	service := &serviceRequest{request: scope.request, pathGrant: true}
	if barrier.Kind == "visibility" {
		body, err := readModelBody(request.Body) // SAME decoded body limit and clearable replay owner.
		if err != nil {
			return "json"
		}
		fields, err := serviceJSONFields(body)
		if err != nil {
			clear(body)
			return "json"
		}
		request.Body = newModelBody(body)
		request.ContentLength = int64(len(body))
		request.Header.Del("Content-Length")
		_, visibility := fields["visibility"]
		_, private := fields["private"]
		service.pathGrant = visibility || private
	}
	if err := s.policyMu.LockContext(scope.ctx); err != nil {
		return "cancelled"
	}
	defer s.policyMu.Unlock()
	if !scope.allowed(s.currentEngine(), s.dialer) || !service.allowed(s.currentEngine(), scope.permit) {
		return "path"
	}
	scope.mu.Lock()
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
