package egress

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/http2"
)

func physicalH2OwnedWriterFixture(t *testing.T, wrap ...func(net.Conn) net.Conn) (*physicalH2Conn, *tls.Conn, *physicalH2Delivery, *ModelUsageMeterV1, <-chan ModelUsageCompletionV1) {
	t.Helper()
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
	guest, native := net.Pipe()
	_ = guest.SetDeadline(time.Now().Add(5 * time.Second))
	_ = native.SetDeadline(time.Now().Add(5 * time.Second))
	if len(wrap) != 0 {
		native = wrap[0](native)
	}
	server := tls.Server(native, &tls.Config{MinVersion: tls.VersionTLS12, NextProtos: []string{"h2"}, Certificates: []tls.Certificate{*certificate}})
	client := tls.Client(guest, &tls.Config{MinVersion: tls.VersionTLS12, NextProtos: []string{"h2"}, ServerName: "reader.example.test", RootCAs: roots})
	handshake := make(chan error, 1)
	go func() { handshake <- server.HandshakeContext(t.Context()) }()
	if err := client.HandshakeContext(t.Context()); err != nil || <-handshake != nil {
		t.Fatal("actual original TLS handshake failed", err)
	}
	conn, err := newPhysicalH2Conn(t.Context(), server)
	if err != nil {
		t.Fatal(err)
	}
	conn.owner.scheduler = true
	t.Cleanup(func() { _ = client.NetConn().Close(); conn.joinPhysicalH2() })
	stream, _, err := conn.owner.introduce(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	request := &http.Request{Header: make(http.Header)}
	value, err := conn.owner.key.token(1)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set(physicalH2TokenHeader, value)
	if _, _, err := conn.owner.claim(request.WithContext(t.Context())); err != nil {
		t.Fatal(err)
	}
	meter := NewModelUsageMeterV1()
	turn, refusal, err := meter.BeginV1(ModelUsageRequestV1{Wire: "openai-responses", Path: "/v1/responses", Model: "original"}, 0)
	completed := make(chan ModelUsageCompletionV1, 1)
	if err != nil || refusal != nil || stream.retain(turn, func(completion ModelUsageCompletionV1) { completed <- completion }) != nil {
		t.Fatal("original writer turn admission failed")
	}
	stream.handlerFinished(true)
	return conn, client, stream, meter, completed
}

type physicalH2CloseGate struct {
	net.Conn
	armed   atomic.Bool
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (g *physicalH2CloseGate) Close() error {
	if g.armed.Load() {
		g.once.Do(func() { close(g.entered) })
		<-g.release
	}
	return g.Conn.Close()
}

func TestPhysicalH2FailedWriterKeepsQueuedNativeWriterBehindActualClose(t *testing.T) {
	gate := &physicalH2CloseGate{entered: make(chan struct{}), release: make(chan struct{})}
	conn, client, _, _, _ := physicalH2OwnedWriterFixture(t, func(native net.Conn) net.Conn { gate.Conn = native; return gate })
	release := sync.OnceFunc(func() { close(gate.release) })
	t.Cleanup(release)
	gate.armed.Store(true)
	var encoded bytes.Buffer
	if err := http2.NewFramer(&encoded, nil).WriteData(1, true, []byte("payload")); err != nil {
		t.Fatal(err)
	}
	conn.plainWrite = func([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
	failed := make(chan error, 1)
	go func() { _, err := conn.Write(encoded.Bytes()); failed <- err }()
	waitHTTPProgress(t, gate.entered, "the failed writer entering its original native close")
	// Gate the original net.Conn.Close itself. Cancellation has not yet run.
	// The writer mutex must still prevent a previously admitted native writer
	// from appending bytes while that actual physical cut is outstanding.
	if conn.writeMu.TryLock() {
		conn.writeMu.Unlock()
		t.Fatal("failed writer unlocked before its actual native socket was cut")
	}
	queued := make(chan error, 1)
	read := make(chan error, 1)
	go func() {
		var byte [1]byte
		_, err := client.Read(byte[:])
		if err == nil {
			err = errPhysicalH2Binding
		}
		read <- err
	}()
	go func() {
		// Exercise the same serialized native boundary used by an already
		// admitted frame; do not run a new request through a closed owner.
		conn.writeMu.Lock()
		_, err := conn.actual.Write([]byte("late"))
		conn.writeMu.Unlock()
		queued <- err
	}()
	select {
	case err := <-failed:
		t.Fatal("failed Write returned before original native closure", err)
	case err := <-queued:
		t.Fatal("queued native writer entered before the physical cut", err)
	case <-time.After(50 * time.Millisecond):
	}
	release()
	select {
	case err := <-failed:
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("failed writer did not return after physical closure")
	}
	select {
	case err := <-queued:
		if err == nil {
			t.Fatal("queued native writer appended bytes after physical closure")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("queued native writer did not join after physical closure")
	}
	if err := <-read; errors.Is(err, errPhysicalH2Binding) {
		t.Fatal("late native bytes escaped the original cut")
	}
}

func TestPhysicalH2FailedNativePrefixCutsOriginalWriterBeforeReturning(t *testing.T) {
	conn, client, stream, meter, completed := physicalH2OwnedWriterFixture(t)
	const body = `{"usage":{"total_tokens":7}}`
	var encoded bytes.Buffer
	if err := http2.NewFramer(&encoded, nil).WriteData(1, true, []byte(body)); err != nil {
		t.Fatal(err)
	}
	const cut = 12 // the complete native header and three actual body bytes
	wire := bytes.Clone(encoded.Bytes())
	remaining := cut
	conn.plainWrite = func(payload []byte) (int, error) {
		allowed := min(remaining, len(payload))
		if allowed == 0 {
			return 0, io.ErrUnexpectedEOF
		}
		count, err := conn.actual.Write(payload[:allowed])
		remaining -= count
		if err == nil && allowed != len(payload) {
			err = io.ErrUnexpectedEOF
		}
		return count, err
	}
	conn.owner.mu.Lock()
	stream.selected = 1 // one original scheduler selection at native entry
	conn.owner.mu.Unlock()
	read := make(chan error, 1)
	go func() {
		prefix := make([]byte, cut)
		if _, err := io.ReadFull(client, prefix); err != nil {
			read <- err
			return
		}
		if !bytes.Equal(prefix, wire[:cut]) {
			read <- errPhysicalH2Binding
			return
		}
		var extra [1]byte
		_, err := client.Read(extra[:])
		var timeout net.Error
		if err == nil || errors.As(err, &timeout) && timeout.Timeout() {
			read <- errPhysicalH2Binding
			return
		}
		read <- nil
	}()
	count, err := conn.Write(wire)
	if count != cut || !errors.Is(err, io.ErrUnexpectedEOF) || conn.ctx.Err() == nil {
		t.Fatal("failed physical prefix left the original writer live", count, err)
	}
	select {
	case err := <-read:
		if err != nil {
			t.Fatal("actual native writer was not cut after its exact prefix", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("failed native prefix could still accept later bytes")
	}
	if meter.SnapshotV1().Total.Requests != 0 {
		t.Fatal("failed selected frame released its owner before the original join")
	}
	conn.joinPhysicalH2()
	select {
	case completion := <-completed:
		if !completion.Estimated || completion.Counts.TotalTokens != 1 {
			t.Fatal("failed native prefix lost or promoted its three actual payload bytes")
		}
	default:
		t.Fatal("original failed writer finaliser did not join")
	}
}

func TestPhysicalH2TerminalFrameKeepsLaterSelectedControlUntilNativeCompletion(t *testing.T) {
	conn, client, stream, meter, completed := physicalH2OwnedWriterFixture(t)
	const body = `{"usage":{"total_tokens":7}}`
	var encoded bytes.Buffer
	if err := http2.NewFramer(&encoded, nil).WriteData(1, true, []byte(body)); err != nil {
		t.Fatal(err)
	}
	terminal := bytes.Clone(encoded.Bytes())
	encoded.Reset()
	if err := http2.NewFramer(&encoded, nil).WriteWindowUpdate(1, 16384); err != nil {
		t.Fatal(err)
	}
	control := bytes.Clone(encoded.Bytes())
	conn.owner.mu.Lock()
	stream.selected = 2 // terminal DATA and an already selected stream control
	conn.owner.mu.Unlock()
	read := make(chan error, 1)
	go func() {
		wire := make([]byte, len(terminal)+len(control))
		_, err := io.ReadFull(client, wire)
		if err == nil && !bytes.Equal(wire, append(bytes.Clone(terminal), control...)) {
			err = errPhysicalH2Binding
		}
		read <- err
	}()
	if _, err := conn.Write(terminal); err != nil {
		t.Fatal(err)
	}
	conn.owner.mu.Lock()
	early := stream.finished
	conn.owner.mu.Unlock()
	if early || meter.SnapshotV1().Total.Requests != 0 {
		t.Fatal("END_STREAM released an outstanding selected native control")
	}
	if _, err := conn.Write(control); err != nil {
		t.Fatal(err)
	}
	if err := <-read; err != nil {
		t.Fatal("original selected frames changed", err)
	}
	select {
	case completion := <-completed:
		if completion.Estimated || completion.Counts.TotalTokens != 7 {
			t.Fatal("completed selected frames lost reported usage")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("completed native stream control retained the original turn")
	}
}
