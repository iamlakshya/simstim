package egress

import (
	"bytes"
	"context"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

type physicalPendingWrite struct {
	id      uint32
	release chan struct{}
}
type physicalPendingWriter struct {
	net.Conn
	mu      sync.Mutex
	target  uint32
	ctx     context.Context
	pending chan physicalPendingWrite
}

func (w *physicalPendingWriter) Write(payload []byte) (int, error) {
	reader := bytes.NewReader(payload)
	framer := http2.NewFramer(io.Discard, reader)
	var data uint32
	for reader.Len() >= 9 {
		header, err := framer.ReadFrameHeader()
		if err != nil {
			break
		}
		if header.Type == http2.FrameData {
			data = header.StreamID
			break
		}
		if int(header.Length) > reader.Len() {
			break
		}
		_, _ = reader.Seek(int64(header.Length), io.SeekCurrent)
	}
	w.mu.Lock()
	gate := data != 0 && data == w.target
	if gate {
		w.target = 0
	}
	w.mu.Unlock()
	if gate {
		release := make(chan struct{})
		select {
		case w.pending <- physicalPendingWrite{id: data, release: release}:
		case <-w.ctx.Done():
			return 0, w.ctx.Err()
		}
		select {
		case <-release:
		case <-w.ctx.Done():
			return 0, w.ctx.Err()
		}
	}
	return w.Conn.Write(payload)
}

func TestPhysicalH2SelectedNativeWritesStayOwnedAndDroppedFlowCreditSupportsLaterRequests(t *testing.T) {
	ca, err := newSessionAuthority(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := ca.certificate("reader.example.test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca.cert)
	conn, client := physicalReaderResetPair(t, certificate, roots)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	view := &physicalPendingWriter{Conn: conn, pending: make(chan physicalPendingWrite, 1), ctx: ctx}
	meter := NewModelUsageMeterV1()
	started := make(chan *physicalH2Delivery, 1)
	completed := make(chan ModelUsageCompletionV1, 8)
	const rounds = 6 // six discarded 16KiB selections exceed the original 65535 connection window
	large := `{"padding":"` + strings.Repeat("x", 32<<10) + `","usage":{"total_tokens":7}}`
	handler := conn.deliveryHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stream := physicalH2RequestDelivery(r)
		turn, refusal, err := meter.BeginV1(ModelUsageRequestV1{Wire: "openai-responses", Path: "/v1/responses", Model: "original"}, 0)
		if err != nil || refusal != nil || stream.retain(turn, func(completion ModelUsageCompletionV1) { completed <- completion }) != nil {
			t.Error("original turn admission failed")
			return
		}
		started <- stream
		body := large
		if stream.id == 2*rounds+1 {
			body = `{"usage":{"total_tokens":7}}`
		}
		_, err = io.WriteString(w, body)
		stream.completionEvidence(err == nil && r.Context().Err() == nil)
	}))
	base := &http.Server{Handler: handler, MaxHeaderBytes: maxHTTPHeaderBytes + physicalH2TokenOverhead, HTTP2: &http.HTTP2Config{MaxConcurrentStreams: maxActiveFlows, MaxReadFrameSize: physicalH2OriginalFrameLimit, MaxDecoderHeaderTableSize: physicalH2OriginalTableLimit}}
	server := &http2.Server{NewWriteScheduler: func() http2.WriteScheduler {
		return &physicalH2Scheduler{WriteScheduler: http2.NewRandomWriteScheduler(), owner: conn.owner}
	}}
	served := make(chan struct{})
	go func() {
		server.ServeConn(&physicalH2NativeTLSView{Conn: view, owner: conn.owner}, &http2.ServeConnOpts{Context: ctx, BaseConfig: base, Handler: handler})
		close(served)
	}()
	events := make(chan physicalH2TestEvent, 128)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		reader := http2.NewFramer(io.Discard, client)
		reader.ReadMetaHeaders = hpack.NewDecoder(physicalH2OriginalTableLimit, nil)
		for {
			frame, err := reader.ReadFrame()
			if err != nil {
				events <- physicalH2TestEvent{err: err}
				return
			}
			event := physicalH2TestEvent{kind: frame.Header().Type, stream: frame.Header().StreamID}
			switch frame := frame.(type) {
			case *http2.DataFrame:
				event.payload, event.ended = bytes.Clone(frame.Data()), frame.StreamEnded()
			case *http2.MetaHeadersFrame:
				event.ended = frame.StreamEnded()
			}
			events <- event
		}
	}()
	t.Cleanup(func() {
		cancel()
		_ = client.NetConn().Close()
		_ = conn.Close()
		select {
		case <-served:
		case <-time.After(5 * time.Second):
			t.Error("original SDK server did not join")
		}
		select {
		case <-readerDone:
		case <-time.After(5 * time.Second):
			t.Error("original native guest reader did not join")
		}
	})
	if _, err := io.WriteString(client, http2.ClientPreface); err != nil {
		t.Fatal(err)
	}
	writer := http2.NewFramer(client, nil)
	if err := writer.WriteSettings(); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-events:
		if event.err != nil || event.kind != http2.FrameSettings {
			t.Fatal("original settings missing", event.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("original settings stalled")
	}
	if err := writer.WriteSettingsAck(); err != nil {
		t.Fatal(err)
	}
	var block bytes.Buffer
	encoder := hpack.NewEncoder(&block)
	fields := []hpack.HeaderField{{Name: ":method", Value: "POST"}, {Name: ":scheme", Value: "https"}, {Name: ":path", Value: "/v1/responses"}, {Name: ":authority", Value: "reader.example.test"}}
	for round := 0; round < rounds; round++ {
		id := uint32(2*round + 1)
		view.mu.Lock()
		view.target = id
		view.mu.Unlock()
		if err := writer.WriteHeaders(http2.HeadersFrameParam{StreamID: id, BlockFragment: physicalH2Fields(t, encoder, &block, fields), EndHeaders: true, EndStream: true}); err != nil {
			t.Fatal(err)
		}
		var stream *physicalH2Delivery
		select {
		case stream = <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("original stream never started")
		}
		var pending physicalPendingWrite
		select {
		case pending = <-view.pending:
		case <-time.After(5 * time.Second):
			t.Fatal("selected DATA never reached the original native writer; discarded connection credit was lost")
		}
		if pending.id != id {
			t.Fatal("native writer ownership changed")
		}
		var release sync.Once
		t.Cleanup(func() { release.Do(func() { close(pending.release) }) })
		stream.invalidate()
		waitHTTPState(t, func() bool {
			stream.owner.mu.Lock()
			defer stream.owner.mu.Unlock()
			return stream.serverClosed && stream.returned
		}, "the original SDK stream purge and returned handler")
		stream.owner.mu.Lock()
		early := stream.finished
		stream.owner.mu.Unlock()
		if early || meter.SnapshotV1().Total.Requests != uint64(round) {
			t.Fatal("selected but unentered native DATA released its Source before disposition")
		}
		release.Do(func() { close(pending.release) })
		select {
		case completion := <-completed:
			if !completion.Estimated || completion.Counts.TotalTokens != 0 {
				t.Fatal("cancelled DATA was forwarded or counted without a native write")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("original selected DATA disposition never joined")
		}
		reset := false
		for !reset {
			select {
			case event := <-events:
				if event.err != nil {
					t.Fatal("selected-write cancellation corrupted the permitted connection", event.err)
				}
				if event.stream == id && len(event.payload) != 0 {
					t.Fatal("cancelled selected DATA crossed the original native connection")
				}
				reset = event.kind == http2.FrameRSTStream && event.stream == id
			case <-time.After(5 * time.Second):
				t.Fatal("original native reset missing")
			}
		}
	}
	id := uint32(2*rounds + 1)
	if err := writer.WriteHeaders(http2.HeadersFrameParam{StreamID: id, BlockFragment: physicalH2Fields(t, encoder, &block, fields), EndHeaders: true, EndStream: true}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("later valid request was disabled")
	}
	var answer []byte
	for {
		select {
		case event := <-events:
			if event.err != nil {
				t.Fatal(err)
			}
			if event.stream == id {
				answer = append(answer, event.payload...)
				if event.ended {
					goto complete
				}
			}
		case <-time.After(5 * time.Second):
			t.Fatal("later valid request lost its original connection flow credit")
		}
	}
complete:
	if string(answer) != `{"usage":{"total_tokens":7}}` {
		t.Fatal("later valid answer changed")
	}
	select {
	case completion := <-completed:
		if completion.Estimated || completion.Counts.TotalTokens != 7 {
			t.Fatal("later valid turn lost reported usage")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("later original finaliser stalled")
	}
}
