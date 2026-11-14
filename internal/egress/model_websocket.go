package egress

import (
	"context"
	"io"
)

// This private error carries only our formatted, scrubbed refusal line. The
// shared frame engine never exposes arbitrary filter or provider errors.
type modelWebSocketRefusal struct{ line string }

func (e modelWebSocketRefusal) Error() string { return e.line }

func (s *Server) inspectModelWebSocket(scope *originRequest, handshake *modelRequest, opcode byte, input io.ReadCloser) (io.ReadCloser, error) {
	if opcode >= webSocketClose {
		return input, nil
	}
	refuse := func(feature string) (io.ReadCloser, error) {
		s.emitAudit(s.formatAudit(AuditEntry{Host: scope.request.Host, Port: scope.request.Port,
			Mode: "intercept", Method: scope.request.Method, Path: scope.request.Path,
			Status: 403, Decision: "deny", Rule: "model:" + feature}))
		return nil, modelWebSocketRefusal{line: s.modelRefusalLine(handshake, feature)}
	}
	if opcode != 1 {
		return refuse("model text message")
	}
	body, err := readModelBody(input)
	if err != nil {
		return refuse("complete model JSON")
	}
	model := &modelRequest{request: handshake.request, guard: handshake.guard, webSocket: true}
	if err := model.inspect(body, true); err != nil {
		clear(body)
		return refuse("model request type")
	}
	if err := s.installModelRequest(scope, model); err != nil {
		clear(body)
		return refuse(err.Error())
	}
	return newModelBody(body), nil
}

func identityWebSocketFilter(_ context.Context, _ byte, input io.ReadCloser) (io.ReadCloser, error) {
	return input, nil
}
