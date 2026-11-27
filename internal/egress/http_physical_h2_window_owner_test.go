package egress

import (
	"bytes"
	"io"
	"net/http"
	"strconv"
	"testing"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

func TestPhysicalH2UploadedStreamWindowUpdatesRetireBeforePolicyResetAndKeepSibling(t *testing.T) {
	const uploadBytes = 192 << 10
	meter := NewModelUsageMeterV1()
	started := make(chan *physicalH2Delivery, 2)
	completed := make(chan ModelUsageCompletionV1, 2)
	failures := make(chan error, 2)
	writer, events := physicalH2NativeFixture(t, http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		stream := physicalH2RequestDelivery(request)
		turn, refusal, err := meter.BeginV1(ModelUsageRequestV1{Wire: "openai-responses", Path: "/v1/responses", Model: "uploaded"}, 0)
		if err != nil || refusal != nil || stream.retain(turn, func(completion ModelUsageCompletionV1) { completed <- completion }) != nil {
			failures <- errPhysicalH2Binding
			return
		}
		started <- stream
		count, err := io.Copy(io.Discard, request.Body)
		if err != nil || stream.id == 1 && count != uploadBytes {
			failures <- errPhysicalH2Binding
			return
		}
		_, err = io.WriteString(w, `{"usage":{"total_tokens":7}}`)
		stream.completionEvidence(err == nil)
	}), 0)
	var block bytes.Buffer
	encoder := hpack.NewEncoder(&block)
	fields := []hpack.HeaderField{{Name: ":method", Value: "POST"}, {Name: ":scheme", Value: "https"}, {Name: ":path", Value: "/v1/responses"}, {Name: ":authority", Value: "physical.example.test"}, {Name: "content-length", Value: strconv.Itoa(uploadBytes)}}
	if err := writer.WriteHeaders(http2.HeadersFrameParam{StreamID: 1, BlockFragment: physicalH2Fields(t, encoder, &block, fields), EndHeaders: true}); err != nil {
		t.Fatal(err)
	}
	var stream *physicalH2Delivery
	select {
	case stream = <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("original upload handler did not start")
	}
	chunk := bytes.Repeat([]byte("x"), 16<<10)
	for sent := 0; sent < uploadBytes; sent += len(chunk) {
		if err := writer.WriteData(1, sent+len(chunk) == uploadBytes, chunk); err != nil {
			t.Fatal("original upload did not cross", err)
		}
	}
	// Require a real public-server stream WINDOW_UPDATE to cross the actual
	// TLS writer. An artificial selected count cannot satisfy this oracle.
	for {
		select {
		case err := <-failures:
			t.Fatal(err)
		case event := <-events:
			if event.err != nil {
				t.Fatal("original upload carrier failed", event.err)
			}
			if event.kind == http2.FrameWindowUpdate && event.stream == 1 {
				goto uploaded
			}
		case <-time.After(5 * time.Second):
			t.Fatal("original server never returned stream receive credit")
		}
	}
uploaded:
	waitHTTPState(t, func() bool {
		stream.owner.mu.Lock()
		defer stream.owner.mu.Unlock()
		return stream.returned && !stream.settled
	}, "the uploaded handler returning behind its original zero response window")
	if meter.SnapshotV1().Total.Requests != 0 {
		t.Fatal("buffered upload answer settled before native delivery")
	}
	stream.invalidate()
	select {
	case completion := <-completed:
		if !completion.Estimated || completion.Counts.TotalTokens != 0 {
			t.Fatal("cancelled upload answer gained unforwarded usage")
		}
	case err := <-failures:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("completed native WINDOW_UPDATE retained the cancelled original owner")
	}
	reset := false
	for !reset {
		select {
		case event := <-events:
			if event.err != nil {
				t.Fatal("upload cancellation killed the original carrier", event.err)
			}
			if event.stream == 1 && event.kind == http2.FrameData {
				t.Fatal("cancelled zero-window answer crossed")
			}
			reset = event.stream == 1 && event.kind == http2.FrameRSTStream
		case <-time.After(5 * time.Second):
			t.Fatal("original upload reset did not cross")
		}
	}
	fields[len(fields)-1].Value = "0"
	if err := writer.WriteHeaders(http2.HeadersFrameParam{StreamID: 3, BlockFragment: physicalH2Fields(t, encoder, &block, fields), EndHeaders: true, EndStream: true}); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteWindowUpdate(3, 65535); err != nil {
		t.Fatal(err)
	}
	select {
	case sibling := <-started:
		if sibling.owner != stream.owner || sibling.id != 3 {
			t.Fatal("later sibling lost the original carrier")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("same-connection sibling never started")
	}
	var body []byte
	for {
		select {
		case event := <-events:
			if event.err != nil {
				t.Fatal("same-connection sibling failed", event.err)
			}
			if event.stream == 3 {
				body = append(body, event.payload...)
				if event.ended {
					goto delivered
				}
			}
		case <-time.After(5 * time.Second):
			t.Fatal("same-connection sibling never finished")
		}
	}
delivered:
	if string(body) != `{"usage":{"total_tokens":7}}` {
		t.Fatal("later sibling answer changed")
	}
	select {
	case completion := <-completed:
		if completion.Estimated || completion.Counts.TotalTokens != 7 {
			t.Fatal("later sibling lost its actual usage")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("later sibling owner did not retire")
	}
}
