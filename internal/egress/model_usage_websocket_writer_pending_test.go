package egress

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestWebSocketTerminalParksOnlyItsOriginalUnjoinedWriter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		meter := NewModelUsageMeterV1()
		scope := &originRequest{ctx: context.Background(), request: Request{Host: "api.openai.com", Port: 443, Method: "GET", Path: "/v1/responses"}}
		state := newModelWebSocketUsage(scope).state
		models := make([]*modelRequest, 2)
		for index, lane := range []string{"writing", "ready"} {
			turn, refusal, err := meter.BeginV1(ModelUsageRequestV1{Wire: "openai-responses", Path: "/v1/responses", Model: lane}, 0)
			if err != nil || refusal != nil || turn.beginPhysicalWebSocketRequestV1() != nil {
				t.Fatal(err)
			}
			turn.ResponseV1(ModelUsageWebSocketV1)
			model := &modelRequest{usage: turn}
			if err := model.inspect([]byte(`{"type":"response.create","stream_id":"`+lane+`"}`), true); err != nil {
				t.Fatal(err)
			}
			models[index] = model
			state.records = append(state.records, model)
		}
		models[1].usage.endPhysicalWebSocketRequestV1()
		messages := []string{
			`{"type":"response.created","stream_id":"writing","response":{"id":"response-a"}}`,
			`{"type":"response.completed","stream_id":"writing","response":{"id":"response-a","usage":{"total_tokens":5}}}`,
			`{"type":"response.created","stream_id":"ready","response":{"id":"response-b"}}`,
			`{"type":"response.completed","stream_id":"ready","response":{"id":"response-b","usage":{"total_tokens":7}}}`,
		}
		for _, message := range messages {
			if err := state.messageLocked([]byte(message)); err != nil {
				t.Fatal(err)
			}
		}
		if !models[0].usagePendingFinish || meter.SnapshotV1().Total.Requests != 1 || meter.SnapshotV1().Total.TotalTokens != 7 {
			t.Fatal("one original pending writer blocked or consumed another lane's terminal")
		}
		// Duplicate terminal bytes cannot change the parked first completion.
		if err := state.messageLocked([]byte(`{"type":"response.failed","stream_id":"writing","response":{"id":"response-a","usage":{"total_tokens":999}}}`)); err != nil {
			t.Fatal(err)
		}
		models[0].usage.endPhysicalWebSocketRequestV1()
		state.finishLocked(models[0], false)
		usage := meter.SnapshotV1()
		if usage.Total.Requests != 2 || usage.Total.TotalTokens != 12 || usage.Total.EstimatedRequests != 0 || len(state.records) != 0 {
			t.Fatal("original writer completion lost or changed its parked terminal")
		}
	})
}

func openPhysicalModelWebSocket(t *testing.T, fixture *httpLifetimeFixture) (net.Conn, *bufio.Reader) {
	t.Helper()
	transport := fixture.client.Transport.(*http.Transport)
	guest, err := transport.DialTLSContext(t.Context(), "tcp", "api.openai.com:443")
	if err != nil {
		t.Fatal(err)
	}
	_ = guest.SetDeadline(time.Now().Add(15 * time.Second))
	t.Cleanup(func() { closeHTTPConnection(guest) })
	if _, err := io.WriteString(guest, "GET /v1/responses HTTP/1.1\r\nHost: api.openai.com:443\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(guest)
	response, err := http.ReadResponse(reader, &http.Request{Method: "GET"})
	if err != nil || response.StatusCode != 101 {
		t.Fatal("original checked TLS WebSocket upgrade failed", err)
	}
	return guest, reader
}

func TestActualWebSocketEarlyLaneErrorDoesNotBlockAnotherAnswerWhileOriginWriteStalls(t *testing.T) {
	const kept = `{"type":"response.create","stream_id":"kept","model":"model-b","input":[]}`
	writing := `{"type":"response.create","stream_id":"writing","model":"model-a","input":"` + strings.Repeat("x", 2*webSocketChunk) + `"}`
	const later = `{"type":"response.create","stream_id":"later","model":"model-c","input":[]}`
	const created = `{"type":"response.created","stream_id":"kept","response":{"id":"response-b"}}`
	const early = `{"type":"error","stream_id":"writing","status":400,"error":{"type":"invalid_request_error","message":"authored early response"}}`
	const terminal = `{"type":"response.completed","stream_id":"kept","response":{"id":"response-b","usage":{"total_tokens":7}}}`
	const createdLater = `{"type":"response.created","stream_id":"later","response":{"id":"response-c"}}`
	const terminalLater = `{"type":"response.completed","stream_id":"later","response":{"id":"response-c","usage":{"total_tokens":11}}}`
	resume := make(chan struct{})
	release := sync.OnceFunc(func() { close(resume) })
	t.Cleanup(release)
	originResult := make(chan error, 1)
	fixture := newPhysicalModelLifetimeFixture(t, false, true, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, reader, err := modelUpgradeOrigin(w, r)
		if err != nil {
			originResult <- err
			return
		}
		defer conn.Close()
		request, err := wsReadTestMessage(reader)
		if err != nil || string(request) != kept {
			originResult <- errPhysicalH2Binding
			return
		}
		first, err := wsReadTestFrame(reader)
		if err != nil || first.final || first.opcode != 1 || !strings.Contains(string(first.payload), `"stream_id":"writing"`) {
			originResult <- errPhysicalH2Binding
			return
		}
		// Stop reading the incomplete second request while independent lane
		// events continue through the same real verified TLS connection.
		for _, message := range []string{created, early, terminal} {
			if _, err := conn.Write(wsTestWire(1, true, false, []byte(message))); err != nil {
				originResult <- err
				return
			}
		}
		select {
		case <-resume:
		case <-r.Context().Done():
			return
		}
		whole := append([]byte(nil), first.payload...)
		for {
			frame, err := wsReadTestFrame(reader)
			if err != nil || frame.opcode != 0 {
				originResult <- errPhysicalH2Binding
				return
			}
			whole = append(whole, frame.payload...)
			if frame.final {
				break
			}
		}
		if string(whole) != writing {
			originResult <- errPhysicalH2Binding
			return
		}
		request, err = wsReadTestMessage(reader)
		if err != nil || string(request) != later {
			originResult <- errPhysicalH2Binding
			return
		}
		for _, message := range []string{createdLater, terminalLater} {
			if _, err := conn.Write(wsTestWire(1, true, false, []byte(message))); err != nil {
				originResult <- err
				return
			}
		}
		_, err = conn.Write(wsTestWire(webSocketClose, true, false, []byte{3, 232}))
		originResult <- err
	}))
	t.Cleanup(release)
	guest, reader := openPhysicalModelWebSocket(t, fixture)
	for _, request := range []string{kept, writing} {
		if _, err := guest.Write(wsTestWire(1, true, true, []byte(request))); err != nil {
			t.Fatal(err)
		}
	}
	for _, message := range []string{created, early, terminal} {
		got, err := wsReadTestMessage(reader)
		if err != nil || string(got) != message {
			t.Fatal("an early lane error changed or blocked its admitted sibling", err)
		}
	}
	waitHTTPState(t, func() bool { return fixture.env.srv.modelUsageSnapshot().Total.Requests == 1 }, "the independently completed lane while the original message write is stalled")
	before := fixture.env.srv.modelUsageSnapshot()
	if before.Total.TotalTokens != 7 || before.Total.EstimatedRequests != 0 {
		t.Fatal("the stalled upload discarded its sibling's reported count")
	}
	release()
	waitHTTPState(t, func() bool { return fixture.env.srv.modelUsageSnapshot().Total.Requests == 2 }, "the same original writer to settle its parked error without closing the connection")
	if _, err := guest.Write(wsTestWire(1, true, true, []byte(later))); err != nil {
		t.Fatal(err)
	}
	for _, message := range []string{createdLater, terminalLater} {
		got, err := wsReadTestMessage(reader)
		if err != nil || string(got) != message {
			t.Fatal("later valid operation was changed or disabled", err)
		}
	}
	if _, err := wsReadTestFrame(reader); err != nil {
		t.Fatal(err)
	}
	if err := <-originResult; err != nil {
		t.Fatal(err)
	}
	waitHTTPState(t, func() bool { return fixture.env.srv.modelUsageSnapshot().Total.Requests == 3 }, "the later valid same-connection terminal")
	usage := fixture.env.srv.modelUsageSnapshot()
	if usage.Total.EstimatedRequests != 1 || fixture.guestDials.Load() != 1 || fixture.dials.Load() != 1 {
		t.Fatal("writer retirement lost a turn or reconnected another lane")
	}
	for _, row := range usage.Models {
		if row.Model == "model-b" && (row.Counts.TotalTokens != 7 || row.Counts.EstimatedRequests != 0) {
			t.Fatal("the independent sibling lost its reported count")
		}
		if row.Model == "model-c" && (row.Counts.TotalTokens != 11 || row.Counts.EstimatedRequests != 0) {
			t.Fatal("the later same-connection request lost its reported count")
		}
	}
	if !api.ValidModelUsageV1(usage) {
		t.Fatal("parked completion corrupted exact totals")
	}
}
