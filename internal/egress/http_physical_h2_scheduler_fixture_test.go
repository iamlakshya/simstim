package egress

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

// Both benchmark variants and native scheduler tests use this same verified
// TLS/public-server/physical-owner path. No listening socket is opened.
func physicalH2SchedulerPair(t testing.TB, factory func() http2.WriteScheduler, wrap func(*physicalH2Conn) net.Conn, handler http.Handler) (*physicalH2Conn, *tls.Conn, func()) {
	t.Helper()
	if err := physicalH2HooksError(); err != nil {
		t.Fatal(err)
	}
	ca, err := newSessionAuthority(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := ca.certificate("scheduler.example.test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca.cert)
	guest, native := net.Pipe()
	_ = guest.SetDeadline(time.Now().Add(15 * time.Second))
	_ = native.SetDeadline(time.Now().Add(15 * time.Second))
	actual := tls.Server(native, &tls.Config{MinVersion: tls.VersionTLS12, NextProtos: []string{"h2"}, Certificates: []tls.Certificate{*certificate}})
	client := tls.Client(guest, &tls.Config{MinVersion: tls.VersionTLS12, NextProtos: []string{"h2"}, ServerName: "scheduler.example.test", RootCAs: roots})
	handshake := make(chan error, 1)
	go func() { handshake <- actual.HandshakeContext(t.Context()) }()
	if err := client.HandshakeContext(t.Context()); err != nil || <-handshake != nil {
		t.Fatal("original scheduler TLS handshake failed", err)
	}
	conn, err := newPhysicalH2Conn(t.Context(), actual)
	if err != nil {
		t.Fatal(err)
	}
	conn.owner.scheduler = true
	view := net.Conn(conn)
	if wrap != nil {
		view = wrap(conn)
	}
	base := &http.Server{Handler: conn.deliveryHandler(handler), MaxHeaderBytes: maxHTTPHeaderBytes + physicalH2TokenOverhead,
		HTTP2: &http.HTTP2Config{MaxConcurrentStreams: maxActiveFlows, MaxReadFrameSize: physicalH2OriginalFrameLimit, MaxDecoderHeaderTableSize: physicalH2OriginalTableLimit}}
	server := &http2.Server{}
	if factory != nil {
		server.NewWriteScheduler = func() http2.WriteScheduler { return &physicalH2Scheduler{WriteScheduler: factory(), owner: conn.owner} }
	}
	served := make(chan struct{})
	go func() {
		server.ServeConn(&physicalH2NativeTLSView{Conn: view, owner: conn.owner}, &http2.ServeConnOpts{Context: t.Context(), BaseConfig: base, Handler: base.Handler})
		close(served)
	}()
	closePair := sync.OnceFunc(func() {
		_ = client.NetConn().Close()
		_ = conn.Close()
		select {
		case <-served:
		case <-time.After(5 * time.Second):
			t.Error("original scheduler server did not join")
		}
		conn.joinPhysicalH2()
	})
	return conn, client, closePair
}

type physicalH2SchedulerEvent struct {
	kind    http2.FrameType
	id      uint32
	ended   bool
	payload []byte
	fields  []hpack.HeaderField
	err     error
}

func physicalH2SchedulerGuest(t *testing.T, client *tls.Conn, window uint32) (*http2.Framer, <-chan physicalH2SchedulerEvent) {
	t.Helper()
	events := make(chan physicalH2SchedulerEvent, 16*maxActiveFlows)
	done := make(chan struct{})
	go func() {
		defer close(done)
		reader := http2.NewFramer(io.Discard, client)
		reader.ReadMetaHeaders = hpack.NewDecoder(physicalH2OriginalTableLimit, nil)
		for {
			frame, err := reader.ReadFrame()
			if err != nil {
				events <- physicalH2SchedulerEvent{err: err}
				return
			}
			event := physicalH2SchedulerEvent{kind: frame.Header().Type, id: frame.Header().StreamID}
			switch frame := frame.(type) {
			case *http2.DataFrame:
				event.ended, event.payload = frame.StreamEnded(), bytes.Clone(frame.Data())
			case *http2.MetaHeadersFrame:
				event.ended, event.fields = frame.StreamEnded(), append([]hpack.HeaderField(nil), frame.Fields...)
			}
			events <- event
		}
	}()
	t.Cleanup(func() {
		_ = client.NetConn().Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("scheduler guest reader did not join")
		}
	})
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
			t.Fatal("original scheduler settings missing", event.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("original scheduler settings stalled")
	}
	if err := writer.WriteSettingsAck(); err != nil {
		t.Fatal(err)
	}
	return writer, events
}

type physicalH2Selection struct {
	id      uint32
	data    int
	control bool
}
type physicalH2SelectionTrace struct {
	*physicalH2RoundRobin
	pushed chan physicalH2Selection
	popped chan physicalH2Selection
}

func (s *physicalH2SelectionTrace) Push(frame http2.FrameWriteRequest) {
	control := s.control(frame)
	s.physicalH2RoundRobin.Push(frame)
	s.pushed <- physicalH2Selection{frame.StreamID(), frame.DataSize(), control}
}

func (s *physicalH2SelectionTrace) Pop() (http2.FrameWriteRequest, bool) {
	frame, ok := s.physicalH2RoundRobin.Pop()
	if ok {
		s.popped <- physicalH2Selection{frame.StreamID(), frame.DataSize(), s.control(frame)}
	}
	return frame, ok
}

func newPhysicalH2SelectionTrace() *physicalH2SelectionTrace {
	return &physicalH2SelectionTrace{physicalH2RoundRobin: newPhysicalH2RoundRobin(), pushed: make(chan physicalH2Selection, 16*maxActiveFlows), popped: make(chan physicalH2Selection, 16*maxActiveFlows)}
}
