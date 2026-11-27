package egress

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"golang.org/x/net/http2"
)

func TestPhysicalH2CountsEveryActualTLSWriteCutWithoutFrameOrPaddingBytes(t *testing.T) {
	const body = `{"usage":{"total_tokens":7}}`
	var encoded bytes.Buffer
	if err := http2.NewFramer(&encoded, nil).WriteDataPadded(1, true, []byte(body), []byte{0, 0, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	wire := bytes.Clone(encoded.Bytes())
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
	for cut := 0; cut <= len(wire); cut++ {
		guest, native := net.Pipe()
		t.Cleanup(func() { _ = guest.Close(); _ = native.Close() })
		_ = guest.SetDeadline(time.Now().Add(5 * time.Second))
		_ = native.SetDeadline(time.Now().Add(5 * time.Second))
		server := tls.Server(native, &tls.Config{MinVersion: tls.VersionTLS12, NextProtos: []string{http2.NextProtoTLS}, Certificates: []tls.Certificate{*certificate}})
		client := tls.Client(guest, &tls.Config{MinVersion: tls.VersionTLS12, NextProtos: []string{http2.NextProtoTLS}, ServerName: "physical.example.test", RootCAs: roots})
		handshake := make(chan error, 1)
		go func() { handshake <- server.HandshakeContext(t.Context()) }()
		if err := client.HandshakeContext(t.Context()); err != nil || <-handshake != nil {
			t.Fatal("actual local TLS handshake failed", err)
		}
		conn, err := newPhysicalH2Conn(t.Context(), server)
		if err != nil {
			t.Fatal(err)
		}
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
		request = request.WithContext(t.Context())
		if _, _, err := conn.owner.claim(request); err != nil {
			t.Fatal(err)
		}
		meter := NewModelUsageMeterV1()
		turn, refusal, err := meter.BeginV1(ModelUsageRequestV1{Wire: "openai-responses", Path: "/v1/responses", Model: "original-turn"}, 0)
		if err != nil || refusal != nil || stream.retain(turn, nil) != nil {
			t.Fatal("original meter turn could not attach", err)
		}
		stream.handlerFinished(true) // reproduce a complete buffered handler before transport delivery
		remaining := cut
		conn.plainWrite = func(payload []byte) (int, error) {
			if remaining == 0 {
				return 0, io.ErrClosedPipe
			}
			allowed := min(remaining, len(payload))
			count, err := server.Write(payload[:allowed])
			remaining -= count
			if err == nil && allowed != len(payload) {
				err = io.ErrClosedPipe
			}
			return count, err
		}
		actual := make([]byte, cut)
		read := make(chan error, 1)
		go func() { _, err := io.ReadFull(client, actual); read <- err }()
		_, writeErr := conn.Write(wire)
		if err := <-read; err != nil || !bytes.Equal(actual, wire[:cut]) {
			t.Fatalf("cut%d did not cross the actual TLS writer as an exact prefix: %v", cut, err)
		}
		if cut != len(wire) && writeErr == nil {
			t.Fatalf("cut%d was incorrectly accepted as a whole physical frame", cut)
		}
		conn.joinPhysicalH2()
		_ = client.NetConn().Close()
		usage := meter.SnapshotV1()
		if cut == len(wire) {
			if usage.Total.Requests != 1 || usage.Total.EstimatedRequests != 0 || usage.Total.TotalTokens != 7 {
				t.Fatal("complete actual padded DATA did not report exactly once")
			}
			continue
		}
		prefix := max(0, min(cut-10, len(body))) // nine frame bytes and one pad-length byte
		estimate, _ := modelUsageEstimateV1(0, uint64(prefix))
		if usage.Total.Requests != 1 || usage.Total.EstimatedRequests != 1 || usage.Total.TotalTokens != estimate || !api.ValidModelUsageV1(usage) {
			t.Fatalf("cut%d counted buffering, framing or padding, or lost a successful payload prefix", cut)
		}
	}
}
