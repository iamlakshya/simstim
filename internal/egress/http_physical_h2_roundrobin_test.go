package egress

import (
	"bytes"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

func TestPhysicalH2RoundRobinServes128ReadyStreamsAndPrioritisesBlockedStreamReset(t *testing.T) {
	trace := newPhysicalH2SelectionTrace()
	meter := NewModelUsageMeterV1()
	const active = maxActiveFlows
	const answerEnd = `","usage":{"total_tokens":7}}`
	answer := `{"padding":"` + strings.Repeat("x", 48<<10) + answerEnd
	answerBytes := []byte(answer)
	controllers := make(chan *http.ResponseController, 1)
	failures := make(chan error, active)
	var pendingWriter *physicalPendingWriter
	conn, client, closePair := physicalH2SchedulerPair(t, func() http2.WriteScheduler { return trace }, func(conn *physicalH2Conn) net.Conn {
		pendingWriter = &physicalPendingWriter{Conn: conn, target: 1, ctx: conn.ctx, pending: make(chan physicalPendingWrite, 1)}
		return pendingWriter
	}, http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		stream := physicalH2RequestDelivery(request)
		turn, refusal, err := meter.BeginV1(ModelUsageRequestV1{Wire: "openai-responses", Path: "/v1/responses", Model: "round-robin"}, 0)
		if err != nil || refusal != nil || stream.retain(turn, nil) != nil {
			failures <- errPhysicalH2Binding
			return
		}
		if stream.id == 3 {
			controllers <- http.NewResponseController(w)
		}
		_, err = w.Write(answerBytes)
		stream.completionEvidence(err == nil && request.Context().Err() == nil)
	}))
	t.Cleanup(closePair)
	writer, events := physicalH2SchedulerGuest(t, client, 65535)
	var block bytes.Buffer
	encoder := hpack.NewEncoder(&block)
	fields := []hpack.HeaderField{{Name: ":method", Value: "GET"}, {Name: ":scheme", Value: "https"}, {Name: ":authority", Value: "scheduler.example.test"}, {Name: ":path", Value: "/v1/responses"}}
	send := func(id uint32) {
		if err := writer.WriteHeaders(http2.HeadersFrameParam{StreamID: id, BlockFragment: physicalH2Fields(t, encoder, &block, fields), EndHeaders: true, EndStream: true}); err != nil {
			t.Fatal(err)
		}
	}
	send(1)
	var pending physicalPendingWrite
	select {
	case pending = <-pendingWriter.pending:
	case <-time.After(5 * time.Second):
		t.Fatal("first original DATA selection never entered its native gate")
	}
	open := sync.OnceFunc(func() { close(pending.release) })
	t.Cleanup(open)
	// Keep stream3 blocked, while every other admitted stream has enough
	// original flow credit for its whole answer. No DATA is preconsumed.
	if err := writer.WriteSettings(http2.Setting{ID: http2.SettingInitialWindowSize, Val: 0}); err != nil {
		t.Fatal(err)
	}
	for index := 1; index < active; index++ {
		send(uint32(2*index + 1))
	}
	ready := make(map[uint32]int, active)
	for len(ready) != active {
		select {
		case event := <-trace.pushed:
			if event.data > 0 {
				ready[event.id] = event.data
			}
		case err := <-failures:
			t.Fatal(err)
		case <-time.After(5 * time.Second):
			t.Fatal("128 original handlers did not queue their real DATA")
		}
	}
	if pending.length != 16<<10 || ready[1] <= int(pending.length) {
		t.Fatal("native fairness fixture did not retain an original DATA remainder behind its selected 16KiB frame")
	}
	for index := 0; index < active; index++ {
		id := uint32(2*index + 1)
		if id != 3 {
			if err := writer.WriteWindowUpdate(id, 65535); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.WriteWindowUpdate(0, 16<<20); err != nil {
		t.Fatal(err)
	}
	var controller *http.ResponseController
	select {
	case controller = <-controllers:
	case <-time.After(5 * time.Second):
		t.Fatal("original blocked stream controller missing")
	}
	if err := controller.SetWriteDeadline(time.Now()); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case event := <-trace.pushed:
			if event.id == 3 && event.control {
				goto resetQueued
			}
		case <-time.After(5 * time.Second):
			t.Fatal("public classifier did not retain the active blocked stream RST as control")
		}
	}
resetQueued:
	// The one gated DATA was selected before the other streams existed.
	// Remove only those already observed pre-gate selections from the log.
	for {
		select {
		case <-trace.popped:
			continue
		default:
			goto drained
		}
	}
drained:
	open()
	seen := make(map[uint32]bool, active)
	resetSelected := false
	for len(seen) != active-1 {
		select {
		case event := <-trace.popped:
			if event.id == 0 {
				continue
			}
			if !resetSelected {
				if event.id != 3 || !event.control {
					t.Fatal("ready DATA or headers overtook the original blocked stream reset")
				}
				resetSelected = true
				continue
			}
			if event.id == 3 {
				if !event.control {
					t.Fatal("purged blocked DATA was selected after its RST")
				}
				continue
			}
			if seen[event.id] {
				t.Fatal("one continuously ready stream got a second selection before its siblings", event.id)
			}
			seen[event.id] = true
		case err := <-failures:
			t.Fatal(err)
		case <-time.After(5 * time.Second):
			t.Fatal("a ready stream starved behind another stream or blocked flow credit")
		}
	}
	answers := make(map[uint32][]byte, active)
	ended := make(map[uint32]bool, active)
	reset := false
	for len(ended) != active-1 || !reset {
		select {
		case event := <-events:
			if event.err != nil {
				t.Fatal("original round robin TLS carrier failed", event.err)
			}
			if event.id == 3 {
				if event.kind == http2.FrameData {
					t.Fatal("zero-window DATA spent unavailable stream credit")
				}
				reset = event.kind == http2.FrameRSTStream
				continue
			}
			if event.kind == http2.FrameData {
				answers[event.id] = append(answers[event.id], event.payload...)
			}
			if event.id != 0 && event.ended {
				if ended[event.id] {
					t.Fatal("native completion was duplicated")
				}
				ended[event.id] = true
			}
		case err := <-failures:
			t.Fatal(err)
		case <-time.After(5 * time.Second):
			t.Fatal("original ready streams did not finish with unchanged flow credit")
		}
	}
	if len(answers) != active-1 {
		t.Fatal("native scheduler omitted a permitted answer")
	}
	for _, body := range answers {
		if string(body) != answer {
			t.Fatal("native scheduler changed a permitted answer")
		}
	}
	waitHTTPState(t, func() bool { return meter.SnapshotV1().Total.Requests == active }, "all128 original completion callbacks exactly once")
	usage := meter.SnapshotV1()
	if usage.Total.EstimatedRequests != 1 || usage.Total.TotalTokens != 7*(active-1) {
		t.Fatal("classifier or scheduler lost, duplicated or promoted original completion")
	}
	conn.owner.mu.Lock()
	retained := len(conn.owner.streams)
	conn.owner.mu.Unlock()
	if retained > active {
		t.Fatal("scheduler introduced another active-stream bound")
	}
}
