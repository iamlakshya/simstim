package egress

import (
	"bytes"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

func TestPhysicalH2PendingResetCannotSwallowOriginalDeadlineInsideExposedContinuation(t *testing.T) {
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
	prefaced := make(chan struct{})
	exposed := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		preface := make([]byte, len(http2.ClientPreface))
		if _, err := io.ReadFull(conn, preface); err != nil || string(preface) != http2.ClientPreface {
			result <- errPhysicalH2Binding
			return
		}
		close(prefaced)
		reader := http2.NewFramer(io.Discard, conn)
		frame, err := reader.ReadFrame()
		if err != nil {
			result <- err
			return
		}
		header, ok := frame.(*http2.HeadersFrame)
		if !ok || header.StreamID != 3 || header.HeadersEnded() {
			result <- errPhysicalH2Binding
			return
		}
		close(exposed)
		_, err = reader.ReadFrame()
		result <- err
	}()
	if _, err := io.WriteString(client, http2.ClientPreface); err != nil {
		t.Fatal(err)
	}
	waitHTTPProgress(t, prefaced, "the original native preface")
	stream, _, err := conn.owner.introduce(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	var block bytes.Buffer
	encoder := hpack.NewEncoder(&block)
	fields := []hpack.HeaderField{{Name: ":method", Value: "GET"}, {Name: ":scheme", Value: "https"}, {Name: ":path", Value: "/v1/responses"}, {Name: ":authority", Value: "reader.example.test"}}
	encoded := physicalH2Fields(t, encoder, &block, fields)
	writer := http2.NewFramer(client, nil)
	if err := writer.WriteHeaders(http2.HeadersFrameParam{StreamID: 3, BlockFragment: encoded[:len(encoded)/2], EndHeaders: false, EndStream: true}); err != nil {
		t.Fatal(err)
	}
	waitHTTPProgress(t, exposed, "the original incomplete header block crossing before cancellation")
	deadline := time.Now().Add(100 * time.Millisecond)
	if err := conn.SetReadDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	stream.invalidate()
	select {
	case err := <-result:
		var timeout net.Error
		if !errors.As(err, &timeout) || !timeout.Timeout() {
			t.Fatal("pending reset replaced the original deadline or interrupted CONTINUATION", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pending reset swallowed the expired original deadline inside CONTINUATION")
	}
	conn.controlMu.Lock()
	retained := conn.readDeadline.Equal(deadline)
	conn.controlMu.Unlock()
	if !retained || conn.ctx.Err() != nil {
		t.Fatal("private reader changed the caller deadline or forged native closure")
	}
	// As in the original server, the reader owns shutdown after the actual
	// timeout. Only closing and joining that native socket settles its rows.
	conn.joinPhysicalH2()
}
