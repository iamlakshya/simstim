package egress

import (
	"context"
	"io"
)

// This private error carries only our formatted, scrubbed refusal line. The
// shared frame engine never exposes arbitrary filter or provider errors.
type modelWebSocketRefusal struct{ line string }

func (e modelWebSocketRefusal) Error() string { return e.line }

func (s *Server) inspectModelWebSocket(ctx context.Context, scope *originRequest, handshake *modelRequest, opcode byte, input io.ReadCloser) (io.ReadCloser, error) {
	if opcode >= webSocketClose {
		return input, nil
	}
	current := handshake
	refuse := func(feature string) (io.ReadCloser, error) {
		s.emitAudit(s.formatAudit(AuditEntry{Host: scope.request.Host, Port: scope.request.Port,
			Mode: "intercept", Method: scope.request.Method, Path: scope.request.Path,
			Status: 403, Decision: "deny", Rule: s.modelAuditRule(current, feature)}))
		return nil, modelWebSocketRefusal{line: s.modelRefusalLine(current, feature)}
	}
	if opcode != 1 {
		return refuse("model text message")
	}
	body, err := readModelBody(input)
	if err != nil {
		return refuse("complete model JSON")
	}
	model := &modelRequest{request: handshake.request, guard: handshake.guard, webSocket: true}
	current = model
	if err := model.inspect(body, true); err != nil {
		clear(body)
		return refuse("model request type")
	}
	model.requestBytes = uint64(len(body))
	if err := s.installModelRequest(scope, model); err != nil {
		clear(body)
		return refuse(err.Error())
	}
	if err := s.admitModelWebSocketUsage(ctx, scope, model); err != nil {
		clear(body)
		return nil, err
	}
	scope.mu.Lock()
	usage := scope.usageWS
	scope.mu.Unlock()
	return &modelUsageWebSocketRequestBody{ReadCloser: newModelBody(body), turn: model.usage, written: func() { usage.requestWritten(ctx, model) }}, nil
}

func identityWebSocketFilter(_ context.Context, _ byte, input io.ReadCloser) (io.ReadCloser, error) {
	return input, nil
}
