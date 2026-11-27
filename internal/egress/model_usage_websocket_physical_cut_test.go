package egress

import (
	"context"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestWebSocketModelUsageChargesEveryPhysicalGuestWriteCut(t *testing.T) {
	const created = `{"type":"response.created","response":{"id":"response-a"}}`
	const terminal = `{"type":"response.completed","response":{"id":"response-a","usage":{"total_tokens":7}}}`
	const requestBytes = 41
	wire := []byte("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
	type payloadSpan struct{ start, end int }
	var spans []payloadSpan
	appendFrame := func(opcode byte, final bool, payload string, count bool) {
		frame := wsTestWire(opcode, final, false, []byte(payload))
		if count {
			spans = append(spans, payloadSpan{len(wire) + len(frame) - len(payload), len(wire) + len(frame)})
		}
		wire = append(wire, frame...)
	}
	appendFrame(1, true, created, true)
	middle := len(terminal) / 2
	appendFrame(1, false, terminal[:middle], true)
	appendFrame(webSocketPing, true, "control bytes", false)
	appendFrame(0, true, terminal[middle:], true)
	for cut := 0; cut <= len(wire); cut++ {
		t.Run(fmt.Sprint(cut), func(t *testing.T) {
			meter := NewModelUsageMeterV1()
			turn, refusal, err := meter.BeginV1(ModelUsageRequestV1{Wire: "openai-responses", Path: "/v1/responses", Model: "model-a", RequestBytes: requestBytes}, 0)
			if err != nil || refusal != nil {
				t.Fatal("unlimited accounting admission failed", err)
			}
			turn.ResponseV1(ModelUsageWebSocketV1)
			server := &Server{usage: meter, scrubber: NewScrubber(nil), now: time.Now}
			scope := &originRequest{s: server, ctx: context.Background(), request: Request{Host: "api.openai.com", Port: 443, Method: "GET", Path: "/v1/responses"}}
			usage := newModelWebSocketUsage(scope)
			if err := usage.reserve(t.Context()); err != nil || !usage.add(&modelRequest{usage: turn}) {
				t.Fatal("observer reservation failed", err)
			}
			forward := newHTTPForwarding()
			forward.observeModelWebSocket(usage)
			guest, output := net.Pipe()
			defer output.Close()
			_ = guest.SetReadDeadline(time.Now().Add(5 * time.Second))
			_ = output.SetWriteDeadline(time.Now().Add(5 * time.Second))
			written := make(chan int, 1)
			go func() {
				n, _ := (&httpGuestWriter{Conn: output, forward: forward}).Write(wire)
				written <- n
			}()
			accepted := make([]byte, cut)
			if _, err := io.ReadFull(guest, accepted); err != nil {
				t.Fatal("physical pipe did not accept the selected cut", err)
			}
			_ = guest.Close()
			if n := <-written; n != cut {
				t.Fatalf("physical successful-write prefix = %d, want %d", n, cut)
			}
			usage.close()
			got := meter.SnapshotV1()
			if cut == len(wire) {
				if got.Total.Requests != 1 || got.Total.TotalTokens != 7 || got.Total.EstimatedRequests != 0 {
					t.Fatal("complete fragmented terminal did not commit reported usage once")
				}
				return
			}
			var payloadBytes uint64
			for _, span := range spans {
				payloadBytes += uint64(max(0, min(cut, span.end)-span.start))
			}
			estimate, _ := modelUsageEstimateV1(requestBytes, payloadBytes)
			if got.Total.Requests != 1 || got.Total.EstimatedRequests != 1 || got.Total.TotalTokens != estimate || !api.ValidModelUsageV1(got) {
				t.Fatalf("cut %d charged read-ahead or framing, lost a payload byte, or promoted partial usage: %+v", cut, got.Total)
			}
		})
	}
}
