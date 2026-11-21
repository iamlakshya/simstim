package egress

import (
	"bytes"
	"io"
	"net/http"
	"testing"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

func TestPhysicalH2OriginalSDKResetCancelsItsActiveDetachedRequestAndKeepsSibling(t *testing.T) {
	meter := NewModelUsageMeterV1()
	ended := make(chan struct{})
	errors := make(chan error, 2)
	writer, events := physicalH2NativeFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stream := physicalH2RequestDelivery(r)
		turn, refusal, err := meter.BeginV1(ModelUsageRequestV1{Wire: "openai-responses", Path: "/v1/responses", Model: "original"}, 0)
		if err != nil || refusal != nil || stream.retain(turn, nil) != nil {
			errors <- errPhysicalH2Binding
			return
		}
		if stream.id == 1 {
			if err := http.NewResponseController(w).SetWriteDeadline(time.Now()); err != nil {
				errors <- err
				return
			}
			<-r.Context().Done()
			close(ended)
			stream.completionEvidence(false)
			return
		}
		_, err = io.WriteString(w, `{"usage":{"total_tokens":7}}`)
		stream.completionEvidence(err == nil)
	}), 65535)
	var block bytes.Buffer
	encoder := hpack.NewEncoder(&block)
	fields := []hpack.HeaderField{{Name: ":method", Value: "POST"}, {Name: ":scheme", Value: "https"}, {Name: ":path", Value: "/v1/responses"}, {Name: ":authority", Value: "physical.example.test"}}
	for _, id := range []uint32{1, 3} {
		if err := writer.WriteHeaders(http2.HeadersFrameParam{StreamID: id, BlockFragment: physicalH2Fields(t, encoder, &block, fields), EndHeaders: true, EndStream: true}); err != nil {
			t.Fatal(err)
		}
	}
	reset, sibling := false, false
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for !reset || !sibling {
		select {
		case err := <-errors:
			t.Fatal(err)
		case event := <-events:
			if event.err != nil {
				t.Fatal("one SDK reset closed its permitted native sibling", event.err)
			}
			if event.kind == http2.FrameRSTStream && event.stream == 1 {
				reset = true
			}
			if event.stream == 3 && event.ended {
				sibling = true
			}
		case <-deadline.C:
			t.Fatal("original SDK reset or independent sibling stalled")
		}
	}
	waitHTTPProgress(t, ended, "the original active request's detached context to cancel after its real SDK reset")
	waitHTTPState(t, func() bool { return meter.SnapshotV1().Total.Requests == 2 }, "both original physical turn finalisers")
	usage := meter.SnapshotV1()
	if usage.Total.EstimatedRequests != 1 || usage.Total.TotalTokens != 7 {
		t.Fatal("SDK reset lost its cut or downgraded a permitted sibling")
	}
}
