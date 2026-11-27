package egress

import (
	"bytes"
	"io"
	"net"
	"strconv"
	"testing"
	"time"
)

func TestPhysicalH1GuestCutsExcludeHeadersChunksAndTrailers(t *testing.T) {
	const body = `{"usage":{"total_tokens":7}}`
	for _, chunked := range []bool{false, true} {
		wire := []byte("HTTP/1.1 100 Continue\r\n\r\nHTTP/1.1 200 OK\r\nContent-Type: application/json\r\n")
		type span struct{ start, end int }
		var spans []span
		if chunked {
			wire = append(wire, "Transfer-Encoding: chunked\r\nTrailer: X-End\r\n\r\n"...)
			for start := 0; start < len(body); start += 3 {
				part := body[start:min(start+3, len(body))]
				wire = append(wire, strconv.FormatInt(int64(len(part)), 16)+";extension=ignored\r\n"...)
				spans = append(spans, span{len(wire), len(wire) + len(part)})
				wire = append(wire, part+"\r\n"...)
			}
			wire = append(wire, "0\r\nX-End: framing-only\r\n\r\n"...)
		} else {
			wire = append(wire, "Content-Length: "+strconv.Itoa(len(body))+"\r\n\r\n"...)
			spans = append(spans, span{len(wire), len(wire) + len(body)})
			wire = append(wire, body...)
		}
		for cut := 0; cut <= len(wire); cut++ {
			meter := NewModelUsageMeterV1()
			turn, refusal, err := meter.BeginV1(ModelUsageRequestV1{Wire: "openai-responses", Path: "/v1/responses", Model: "original", RequestBytes: 4}, 0)
			if err != nil || refusal != nil {
				t.Fatal(err)
			}
			forward := newHTTPForwarding()
			forward.observeModelHTTP(turn, "POST")
			forward.accepted.Store(uint64(len(wire)))
			guest, native := net.Pipe()
			_ = guest.SetDeadline(time.Now().Add(5 * time.Second))
			_ = native.SetDeadline(time.Now().Add(5 * time.Second))
			written := make(chan int, 1)
			go func() { n, _ := (&httpGuestWriter{Conn: native, forward: forward}).Write(wire); written <- n }()
			actual := make([]byte, cut)
			if _, err := io.ReadFull(guest, actual); err != nil {
				t.Fatal(err)
			}
			_ = guest.Close()
			if n := <-written; n != cut || !bytes.Equal(actual, wire[:cut]) {
				t.Fatalf("cut%d changed the actual guest prefix", cut)
			}
			_ = native.Close()
			var expected uint64
			for _, span := range spans {
				expected += uint64(max(0, min(cut, span.end)-span.start))
			}
			turn.mu.Lock()
			observed := turn.responseBytes
			turn.mu.Unlock()
			if observed != expected {
				t.Fatalf("chunked%t cut%d counted%d body bytes, want%d", chunked, cut, observed, expected)
			}
			complete := cut == len(wire)
			if forward.physicalComplete() != complete {
				t.Fatalf("cut%d changed the native completion fence", cut)
			}
			completion := turn.FinishV1(complete)
			if complete {
				if completion.Estimated || completion.Counts.TotalTokens != 7 {
					t.Fatal("complete physical response lost reported usage")
				}
			} else {
				estimate, _ := modelUsageEstimateV1(4, expected)
				if !completion.Estimated || completion.Counts.TotalTokens != estimate {
					t.Fatalf("cut%d promoted buffered or partial usage", cut)
				}
			}
		}
	}
}

func TestPhysicalH1BodyEveryFragmentKeepsChunkBoundaries(t *testing.T) {
	wire := []byte("POST /v1/responses HTTP/1.1\r\nHost: api.openai.com\r\nTransfer-Encoding: chunked\r\nTrailer: X-End\r\n\r\n4\r\nbody\r\n3\r\nend\r\n0\r\nX-End: ignored\r\n\r\n")
	for split := 0; split <= len(wire); split++ {
		var observed bytes.Buffer
		observer := &physicalHTTP1Body{request: true, observe: func(payload []byte) { _, _ = observed.Write(payload) }}
		if observer.feed(wire[:split]) != nil || observer.feed(wire[split:]) != nil || observed.String() != "bodyend" || observer.state != physicalH1Done {
			t.Fatalf("split%d changed the physical request body", split)
		}
	}
}
