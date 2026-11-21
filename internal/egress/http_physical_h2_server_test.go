package egress

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

type physicalH2TestEvent struct {
	kind     http2.FrameType
	stream   uint32
	ended    bool
	settings []http2.Setting
	payload  []byte
	err      error
}

func physicalH2NativeFixture(t *testing.T, handler http.Handler, window uint32) (*http2.Framer, <-chan physicalH2TestEvent) {
	t.Helper()
	ca, err := newSessionAuthority(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := ca.certificate("physical.example.test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca.cert)
	guest, server := net.Pipe()
	_ = guest.SetDeadline(time.Now().Add(15 * time.Second))
	_ = server.SetDeadline(time.Now().Add(15 * time.Second))
	ctx, cancel := context.WithCancel(t.Context())
	finished := make(chan error, 1)
	go func() {
		finished <- servePhysicalHTTP2Prototype(ctx, tls.Server(server, &tls.Config{MinVersion: tls.VersionTLS12, NextProtos: []string{http2.NextProtoTLS}, Certificates: []tls.Certificate{*certificate}}), handler)
	}()
	client := tls.Client(guest, &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: "physical.example.test", NextProtos: []string{http2.NextProtoTLS}})
	if err := client.HandshakeContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	events := make(chan physicalH2TestEvent, 8*maxActiveFlows)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		defer close(events)
		reader := http2.NewFramer(io.Discard, client)
		reader.ReadMetaHeaders = hpack.NewDecoder(physicalH2OriginalTableLimit, nil)
		reader.MaxHeaderListSize = physicalH2OriginalHeaderLimit
		for {
			frame, err := reader.ReadFrame()
			if err != nil {
				events <- physicalH2TestEvent{err: err}
				return
			}
			event := physicalH2TestEvent{kind: frame.Header().Type, stream: frame.Header().StreamID}
			switch frame := frame.(type) {
			case *http2.DataFrame:
				event.ended, event.payload = frame.StreamEnded(), bytes.Clone(frame.Data())
			case *http2.MetaHeadersFrame:
				event.ended = frame.StreamEnded()
			case *http2.SettingsFrame:
				_ = frame.ForeachSetting(func(setting http2.Setting) error { event.settings = append(event.settings, setting); return nil })
			case *http2.PingFrame:
				event.payload = bytes.Clone(frame.Data[:])
			}
			events <- event
		}
	}()
	if _, err := io.WriteString(client, http2.ClientPreface); err != nil {
		t.Fatal(err)
	}
	writer := http2.NewFramer(client, nil)
	if err := writer.WriteSettings(http2.Setting{ID: http2.SettingInitialWindowSize, Val: window}); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-events:
		if event.err != nil || event.kind != http2.FrameSettings {
			t.Fatal("public standard server did not dispatch above the original TLS connection", event.err)
		}
		found := false
		for _, setting := range event.settings {
			if setting.ID == http2.SettingMaxHeaderListSize {
				found = setting.Val == maxHTTPHeaderBytes+10*32
			}
		}
		if !found {
			t.Fatal("synthetic correlation raised the advertised guest header limit")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("above-TLS standard server never sent its original settings")
	}
	if err := writer.WriteSettingsAck(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		_ = client.NetConn().Close()
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Error("above-TLS standard server did not join its original connection")
		}
		select {
		case <-readerDone:
		case <-time.After(5 * time.Second):
			t.Error("actual guest TLS reader retained its native pipe")
		}
	})
	return writer, events
}

func TestPhysicalH2OriginalStandardServerKeeps128IdenticalRequestsWithOneZeroWindow(t *testing.T) {
	meter := NewModelUsageMeterV1()
	const body = `{"usage":{"total_tokens":7}}`
	errors := make(chan error, maxActiveFlows)
	writer, events := physicalH2NativeFixture(t, http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		stream := physicalH2RequestDelivery(request)
		if stream == nil || request.TLS == nil || request.TLS.NegotiatedProtocol != http2.NextProtoTLS || request.ProtoMajor != 2 || len(request.Header.Values(physicalH2TokenHeader)) != 0 {
			errors <- fmt.Errorf("original handler received missing or exposed delivery metadata")
			return
		}
		turn, refusal, err := meter.BeginV1(ModelUsageRequestV1{Wire: "openai-responses", Path: "/v1/responses", Model: "stream-" + strconv.FormatUint(uint64(stream.id), 10)}, 0)
		if err != nil || refusal != nil {
			errors <- fmt.Errorf("original meter refused an unlimited prototype turn")
			return
		}
		if err := stream.retain(turn, nil); err != nil {
			errors <- err
			return
		}
		_, err = io.WriteString(w, body)
		stream.completionEvidence(err == nil)
	}), 0)
	var block bytes.Buffer
	encoder := hpack.NewEncoder(&block)
	fields := []hpack.HeaderField{{Name: ":method", Value: "POST"}, {Name: ":scheme", Value: "https"}, {Name: ":path", Value: "/v1/responses"}, {Name: ":authority", Value: "physical.example.test"}}
	for index := 0; index < maxActiveFlows; index++ {
		id := uint32(2*index + 1)
		encoded := physicalH2Fields(t, encoder, &block, fields)
		if err := writer.WriteHeaders(http2.HeadersFrameParam{StreamID: id, BlockFragment: encoded, EndHeaders: true, EndStream: true}); err != nil {
			t.Fatal(err)
		}
	}
	for index := 1; index < maxActiveFlows; index++ {
		if err := writer.WriteWindowUpdate(uint32(2*index+1), 65535); err != nil {
			t.Fatal(err)
		}
	}
	ended := make(map[uint32]bool)
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for len(ended) != maxActiveFlows-1 {
		select {
		case err := <-errors:
			t.Fatal(err)
		case event := <-events:
			if event.err != nil {
				t.Fatal(event.err)
			}
			if event.stream == 1 && len(event.payload) != 0 {
				t.Fatal("zero-window stream forwarded decoded payload")
			}
			if event.ended && event.stream != 0 {
				ended[event.stream] = true
			}
		case <-deadline.C:
			t.Fatal("one zero-window response stalled another original active stream")
		}
	}
	poll := time.NewTicker(time.Millisecond)
	defer poll.Stop()
	for {
		got := meter.SnapshotV1()
		if got.Total.Requests == maxActiveFlows-1 {
			if got.Total.EstimatedRequests != 0 || got.Total.TotalTokens != 7*(maxActiveFlows-1) {
				t.Fatal("buffered zero-window response was counted or stream identity was guessed")
			}
			break
		}
		if got.Total.Requests >= maxActiveFlows {
			t.Fatal("zero-window response finished before any physical payload write")
		}
		select {
		case err := <-errors:
			t.Fatal(err)
		case <-poll.C:
		case <-deadline.C:
			t.Fatal("physical completion observer did not finish the unblocked responses")
		}
	}
	if err := writer.WriteRSTStream(1, http2.ErrCodeCancel); err != nil {
		t.Fatal(err)
	}
	for {
		got := meter.SnapshotV1()
		if got.Total.Requests == maxActiveFlows {
			if got.Total.EstimatedRequests != 1 || got.Total.TotalTokens != 7*(maxActiveFlows-1) {
				t.Fatal("RST fence lost a physical prefix or promoted a buffered response")
			}
			for _, model := range got.Models {
				if model.Model == "stream-1" && (model.Counts.EstimatedRequests != 1 || model.Counts.TotalTokens != 0) {
					t.Fatal("identical requests used handler order instead of the original stream")
				}
			}
			break
		}
		select {
		case err := <-errors:
			t.Fatal(err)
		case event := <-events:
			if event.kind == http2.FramePing {
				t.Fatal("private reset-fence ACK escaped to the guest")
			}
			if event.err != nil {
				t.Fatal(event.err)
			}
		case <-poll.C:
		case <-deadline.C:
			t.Fatal("original reset did not settle after its ordered writer fence")
		}
	}
}
