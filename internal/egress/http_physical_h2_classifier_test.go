package egress

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

func TestPhysicalH2PublicClassifierReusesTrailersEmptyDataAndActiveResetWithoutExtraCompletion(t *testing.T) {
	const rounds = 36
	const answer = `{"usage":{"total_tokens":7}}`
	trace := newPhysicalH2SelectionTrace()
	meter := NewModelUsageMeterV1()
	completed := make(chan ModelUsageCompletionV1, rounds)
	controllers := make(chan *http.ResponseController, 1)
	failures := make(chan error, rounds)
	_, client, closePair := physicalH2SchedulerPair(t, func() http2.WriteScheduler { return trace }, nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stream := physicalH2RequestDelivery(r)
		turn, refusal, err := meter.BeginV1(ModelUsageRequestV1{Wire: "openai-responses", Path: "/v1/responses", Model: "classifier"}, 0)
		if err != nil || refusal != nil || stream.retain(turn, func(c ModelUsageCompletionV1) { completed <- c }) != nil {
			failures <- errPhysicalH2Binding
			return
		}
		mode := ((stream.id - 1) / 2) % 3
		switch mode {
		case 0:
			w.Header().Set("Trailer", "X-Complete")
			_, err = io.WriteString(w, answer)
			w.(http.Flusher).Flush()
			w.Header().Set("X-Complete", "original")
		case 1:
			w.(http.Flusher).Flush() // final handler return emits real empty DATA
		case 2:
			controllers <- http.NewResponseController(w)
			_, err = io.WriteString(w, strings.Repeat("x", 32<<10))
		}
		stream.completionEvidence(err == nil && r.Context().Err() == nil)
	}))
	t.Cleanup(closePair)
	writer, events := physicalH2SchedulerGuest(t, client, 0)
	var block bytes.Buffer
	encoder := hpack.NewEncoder(&block)
	fields := []hpack.HeaderField{{Name: ":method", Value: "GET"}, {Name: ":scheme", Value: "https"}, {Name: ":authority", Value: "scheduler.example.test"}, {Name: ":path", Value: "/v1/responses"}}
	for round := 0; round < rounds; round++ {
		id, mode := uint32(2*round+1), round%3
		if err := writer.WriteHeaders(http2.HeadersFrameParam{StreamID: id, BlockFragment: physicalH2Fields(t, encoder, &block, fields), EndHeaders: true, EndStream: true}); err != nil {
			t.Fatal(err)
		}
		if mode == 0 {
			if err := writer.WriteWindowUpdate(id, 65535); err != nil {
				t.Fatal(err)
			}
		}
		if mode == 2 {
			var controller *http.ResponseController
			select {
			case controller = <-controllers:
			case <-time.After(5 * time.Second):
				t.Fatal("original active blocked controller missing")
			}
			for {
				select {
				case event := <-trace.pushed:
					if event.id == id && event.data > 0 {
						goto queued
					}
				case <-time.After(5 * time.Second):
					t.Fatal("original blocked DATA never entered the public scheduler")
				}
			}
		queued:
			if err := controller.SetWriteDeadline(time.Now()); err != nil {
				t.Fatal(err)
			}
		}
		var body []byte
		var ending physicalH2SchedulerEvent
		data, trailer := false, false
		for {
			select {
			case event := <-events:
				if event.err != nil {
					t.Fatal("classifier corrupted the original TLS carrier", event.err)
				}
				if event.id != id {
					continue
				}
				if event.kind == http2.FrameData {
					data = true
					body = append(body, event.payload...)
				}
				for _, field := range event.fields {
					if field.Name == "x-complete" && field.Value == "original" {
						trailer = true
					}
				}
				if event.ended || event.kind == http2.FrameRSTStream {
					ending = event
					goto ended
				}
			case err := <-failures:
				t.Fatal(err)
			case <-time.After(5 * time.Second):
				if mode == 2 {
					t.Fatal("active reset did not retire its original blocked DATA stream")
				}
				t.Fatal("public classifier lost ordering, flow credit or final completion")
			}
		}
	ended:
		switch mode {
		case 0:
			if !data || !trailer || ending.kind != http2.FrameHeaders || string(body) != answer {
				t.Fatal("ordered DATA and original trailers were classified as a reset")
			}
		case 1:
			if !data || ending.kind != http2.FrameData || len(body) != 0 {
				t.Fatal("zero-length DATA was mistaken for a reset or spent flow credit")
			}
		case 2:
			if data || ending.kind != http2.FrameRSTStream {
				t.Fatal("active reset failed to bypass blocked DATA")
			}
		}
		select {
		case completion := <-completed:
			if mode == 0 {
				if completion.Estimated || completion.Counts.TotalTokens != 7 {
					t.Fatal("trailer completion lost its original usage")
				}
			} else if !completion.Estimated || completion.Counts.TotalTokens != 0 {
				t.Fatal("empty or reset completion gained unforwarded payload")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("classifier copy consumed the original completion notification")
		}
		if meter.SnapshotV1().Total.Requests != uint64(round+1) {
			t.Fatal("classifier completed an original request more than once")
		}
		// Native event kind and public request metadata bind the zero-size
		// cases. Classifier reuse must leave headers/trailers/empty DATA
		// ordered, while retaining the active RST as connection control.
		for {
			select {
			case event := <-trace.pushed:
				if event.id == id && event.data == 0 && event.control != (mode == 2) {
					t.Fatal("pinned public constructor changed stream/control retention")
				}
			default:
				goto classified
			}
		}
	classified:
	}
	usage := meter.SnapshotV1()
	if usage.Total.Requests != rounds || usage.Total.EstimatedRequests != 2*rounds/3 || usage.Total.TotalTokens != 7*rounds/3 {
		t.Fatal("reused classifier lost or duplicated original done semantics")
	}
}
