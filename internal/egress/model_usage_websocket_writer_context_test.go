package egress

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestWebSocketOriginalWriterBudgetWaitCancelsBeforeBoundWorkerJoin(t *testing.T) {
	server := newMemberServer(t, InitArgs{SessionID: "writerctx1", Agent: "codex", Policy: api.Policy{Preset: api.PresetLocked}})
	scope := &originRequest{s: server, ctx: context.Background(), request: Request{Host: "api.openai.com", Port: 443, Method: "GET", Path: "/v1/responses"}}
	usage := newModelWebSocketUsage(scope)
	atBudget := make(chan struct{})
	filter := func(ctx context.Context, opcode byte, input io.ReadCloser) (io.ReadCloser, error) {
		if opcode >= webSocketClose {
			return input, nil
		}
		body, err := readModelBody(input)
		if err != nil {
			return nil, err
		}
		turn, refusal, err := server.usage.BeginV1(ModelUsageRequestV1{Wire: "openai-responses", Path: "/v1/responses", Model: "original", RequestBytes: uint64(len(body))}, 0)
		if err != nil || refusal != nil {
			return nil, errPhysicalH2Binding
		}
		if err := turn.beginPhysicalWebSocketRequestV1(); err != nil {
			return nil, err
		}
		model := &modelRequest{usage: turn}
		if err := usage.reserve(ctx); err != nil {
			turn.endPhysicalWebSocketRequestV1()
			return nil, err
		}
		if !usage.add(model) {
			turn.endPhysicalWebSocketRequestV1()
			return nil, io.ErrClosedPipe
		}
		return &modelUsageWebSocketRequestBody{ReadCloser: newModelBody(body), turn: turn, written: func() { close(atBudget); usage.requestWritten(ctx, model) }}, nil
	}
	wrapper, peer, _ := wsTestConnection(t, webSocketFilters{Server: wsPassFilter, Client: filter})
	server.policyMu.Lock()
	unlock := sync.OnceFunc(server.policyMu.Unlock)
	t.Cleanup(unlock) // release before the original fixture's deferred Close
	origin := make(chan error, 1)
	go func() { _, err := wsReadTestMessage(peer); origin <- err }()
	if _, err := wrapper.Write(wsTestWire(1, true, true, []byte(`{}`))); err != nil {
		t.Fatal(err)
	}
	waitHTTPProgress(t, atBudget, "the original client worker's final budget check")
	closed := make(chan error, 1)
	go func() { closed <- wrapper.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("bound Close joined a worker waiting on the live socket context instead of its own cancellation")
	}
	if scope.ctx.Err() != nil {
		t.Fatal("fixture cancelled the socket parent to hide an original-worker deadlock")
	}
	if err := <-origin; err != nil {
		t.Fatal(err)
	}
	unlock()
	usage.close()
	if usage.state.scope.s.modelUsageSnapshot().Total.Requests != 1 {
		t.Fatal("writer cancellation lost the original logical turn")
	}
}
