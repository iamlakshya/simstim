package egress

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
)

func (s *Server) prepareModelHTTPRequest(scope *originRequest, request *http.Request) (*modelRequest, string) {
	kind, _, err := httpClass(scope.request.Host)
	if err != nil || kind != "model" {
		return nil, ""
	}
	guard := s.currentEngine().modelGuard(scope.request)
	if guard == nil {
		return &modelRequest{request: scope.request}, "model path"
	}
	model := &modelRequest{request: scope.request, guard: guard}
	model.webSocket, err = prepareBoundWebSocketRequest(request)
	if err != nil || model.webSocket && (!guard.WebSocket || !strings.HasSuffix(scope.request.Path, "/responses")) {
		return model, "model WebSocket protocol"
	}
	if !model.webSocket {
		for name := range request.Header {
			if strings.EqualFold(name, "Upgrade") {
				return model, "model WebSocket protocol"
			}
		}
		if webSocketHeaderToken(request.Header, "Connection", "upgrade") {
			return model, "model WebSocket protocol"
		}
	}
	model.file = slices.Contains(guard.Files, request.Method+" "+scope.request.Host+scope.request.Path)
	if model.file {
		model.require("files")
	} else if !slices.Contains(guard.Paths, request.Method+" "+scope.request.Path) || strings.Contains(scope.request.Path, "/batches") || request.Method == http.MethodGet && strings.HasSuffix(scope.request.Path, "/responses") && !model.webSocket {
		return model, "model path"
	}
	if request.Method == http.MethodGet {
		input := request.Body
		if input == nil {
			input = http.NoBody
		}
		body, err := readModelBody(input)
		if err != nil || len(body) != 0 {
			clear(body)
			return model, "model GET body"
		}
		request.Body = http.NoBody
		request.ContentLength = 0
		request.Header.Del("Content-Length")
	}
	if !model.webSocket && request.Method == http.MethodPost {
		body, err := readModelBody(request.Body)
		if err != nil {
			return model, "complete model JSON"
		}
		if !model.file {
			if err := model.inspect(body, false); err != nil {
				clear(body)
				return model, "model request type"
			}
		}
		request.Body = newModelBody(body)
		request.ContentLength = int64(len(body))
		request.Header.Del("Content-Length")
	}
	if err := s.installModelRequest(scope, model); err != nil {
		return model, err.Error()
	}
	return model, ""
}

// Inspection completes before the origin transport can see headers or bytes.
// The existing decoded 64 MiB body/message limit applies to identity too.
func readModelBody(input io.ReadCloser) ([]byte, error) {
	if input == nil {
		return nil, io.ErrUnexpectedEOF
	}
	defer input.Close()
	body, err := io.ReadAll(io.LimitReader(input, maxDecodedHTTPBody+1))
	if err != nil || len(body) > maxDecodedHTTPBody {
		clear(body)
		return nil, io.ErrUnexpectedEOF
	}
	return body, nil
}

func (s *Server) installModelRequest(scope *originRequest, model *modelRequest) error {
	if err := s.policyMu.LockContext(scope.ctx); err != nil {
		return fmt.Errorf("model request cancelled")
	}
	defer s.policyMu.Unlock()
	engine := s.currentEngine()
	if !scope.allowed(engine, s.dialer) {
		return fmt.Errorf("model policy")
	}
	scope.mu.Lock()
	reserved := scope.reserved
	scope.mu.Unlock()
	if feature := engine.admitModelRequest(model, scope.permit, !reserved, evalReserve); feature != "" {
		return fmt.Errorf("%s", feature)
	}
	scope.mu.Lock()
	scope.reserved = true
	if previous := scope.model; previous != nil {
		for _, feature := range previous.features {
			model.require(feature)
		}
		model.search = model.search || previous.search
	}
	scope.model = model
	scope.mu.Unlock()
	return nil
}

func (s *Server) modelRefusalLine(model *modelRequest, feature string) string {
	ref := strings.TrimSpace(cleanAuditText(s.sessionRef(), s.scrubber.snapshot(), 128))
	if ref == "" {
		ref = "session"
	}
	hint := "use a supported model request path and JSON type"
	if slices.Contains([]string{"web_fetch", "web_search", "code_execution", "mcp", "url_content", "files"}, feature) {
		hint = fmt.Sprintf("%s policy allow %s --tools %s --in %s", brand.Name, model.request.Host, feature, ref)
	}
	name := feature
	if display := model.display[feature]; display != "" {
		name = cleanAuditText(display, s.scrubber.snapshot(), 96)
	}
	line := fmt.Sprintf("%s: %s may not use %s at %s. Try: %s", brand.Name, ref, name, model.request.Host, hint)
	if model.webSocket && len(line) > 123 {
		shortHint := "Try: use a supported model request."
		if slices.Contains(api.ModelToolNames(), feature) {
			shortHint = "Try: grant " + feature + " in this session's policy."
		}
		suffix := " blocked. " + shortHint
		budget := 123 - len(suffix)
		if len(name) > budget {
			name = name[:budget]
			for !utf8.ValidString(name) {
				name = name[:len(name)-1]
			}
		}
		line = name + suffix
	}
	return line
}

func writeModelRefusal(writer http.ResponseWriter, model *modelRequest, line string) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("X-Should-Retry", "false")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusForbidden)
	var body any
	if model.guard != nil && model.guard.Wire == "anthropic-messages" || model.guard == nil && modelWireForHost(model.request.Host) == "anthropic-messages" {
		var random [16]byte
		_, _ = rand.Read(random[:])
		body = map[string]any{"type": "error", "error": map[string]string{"type": "permission_error", "message": line}, "request_id": "req_" + hex.EncodeToString(random[:])}
	} else {
		body = map[string]any{"error": map[string]any{"message": line, "type": "request_forbidden", "param": nil, "code": nil}}
	}
	_ = json.NewEncoder(writer).Encode(body)
}

func modelWireForHost(host string) string {
	for _, agent := range agents.Table {
		if guard := agent.ModelGuard; guard != nil && slices.Contains(guard.Hosts, host) {
			return guard.Wire
		}
	}
	return ""
}

type modelBody struct {
	mu     sync.Mutex
	reader *bytes.Reader
	bytes  []byte
}

func newModelBody(body []byte) *modelBody {
	return &modelBody{reader: bytes.NewReader(body), bytes: body}
}

func (b *modelBody) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "<model body redacted>")
}
func (b *modelBody) Read(output []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.reader.Read(output)
}
func (b *modelBody) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	clear(b.bytes)
	b.bytes = nil
	b.reader.Reset(nil)
	return nil
}
