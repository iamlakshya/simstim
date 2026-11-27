package egress

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestPhysicalH1UploadCutsCountOnlyTheOriginalCheckedOriginBodyPrefix(t *testing.T) {
	const body = `{"model":"model-a","input":"original-body"}`
	server := newMemberServer(t, InitArgs{SessionID: "upload234", Agent: "codex", Policy: api.Policy{Preset: api.PresetLocked}})
	ca, err := newSessionAuthority(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := ca.certificate("api.openai.com", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca.cert)
	server.transport.TLSClientConfig.RootCAs = roots // same original transport trust configuration
	var encoded bytes.Buffer
	_, _ = io.WriteString(&encoded, "POST /v1/responses HTTP/1.1\r\nHost: api.openai.com:443\r\nContent-Length: "+strconv.Itoa(len(body))+"\r\n\r\n")
	dataStart := encoded.Len()
	_, _ = io.WriteString(&encoded, body)
	wire := bytes.Clone(encoded.Bytes())
	var expected atomic.Int64
	type originResult struct {
		prefix []byte
		err    error
	}
	read := make(chan originResult, 1)
	server.dialer.Lookup = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("198.51.100.8")}, nil
	}
	server.dialer.DialTCP = func(context.Context, netip.AddrPort) (net.Conn, error) {
		client, native := net.Pipe()
		wanted := int(expected.Load())
		_ = client.SetDeadline(time.Now().Add(5 * time.Second))
		_ = native.SetDeadline(time.Now().Add(5 * time.Second))
		t.Cleanup(func() { _ = client.Close(); _ = native.Close() })
		go func() {
			peer := tls.Server(native, &tls.Config{MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"}, Certificates: []tls.Certificate{*certificate}})
			defer peer.NetConn().Close()
			if err := peer.HandshakeContext(t.Context()); err != nil {
				read <- originResult{err: err}
				return
			}
			prefix := make([]byte, wanted)
			_, err := io.ReadFull(peer, prefix)
			read <- originResult{prefix: prefix, err: err}
		}()
		return client, nil
	}
	for cut := 0; cut <= len(wire); cut++ {
		expected.Store(int64(cut))
		ctx, cancel := context.WithCancel(t.Context())
		request := httptest.NewRequest("POST", "https://api.openai.com:443/v1/responses", bytes.NewBufferString(body)).WithContext(ctx)
		canonical, err := canonicalHTTPRequest(request, StreamHeader{Via: "explicit", Host: "api.openai.com", Port: 443}, "https")
		if err != nil {
			t.Fatal(err)
		}
		scope := &originRequest{s: server, request: canonical, scheme: "https", permit: server.currentEngine().NewPermit(), ctx: ctx, cancel: cancel}
		if !scope.track() {
			t.Fatal("original request could not register its real flow")
		}
		model, refusal := server.prepareModelHTTPRequest(scope, request)
		if refusal != "" || model == nil || model.usage == nil {
			t.Fatal("actual model guard did not admit the original request", refusal)
		}
		owned := context.WithValue(ctx, originRequestKey{}, scope)
		raw, err := server.dialHTTPRequest(owned, "tcp", "api.openai.com:443")
		if err != nil {
			t.Fatal(err)
		}
		actual := tls.Client(raw, &tls.Config{MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"}, RootCAs: roots, ServerName: "api.openai.com"})
		if err := actual.HandshakeContext(ctx); err != nil {
			t.Fatal(err)
		}
		observer, err := newPhysicalH2UploadConn(scope, model.usage, actual)
		if err != nil {
			t.Fatal("actual native connection/turn provenance failed", err)
		}
		if count, err := observer.Write(wire[:cut]); err != nil || count != cut {
			t.Fatalf("upload cut%d did not cross the actual checked TLS origin: %d %v", cut, count, err)
		}
		result := <-read
		if result.err != nil || !bytes.Equal(result.prefix, wire[:cut]) {
			t.Fatal("original native origin received another plaintext prefix", result.err)
		}
		_ = observer.Close() // native-write observation joins before the original turn ends
		completion := model.usage.FinishV1(false)
		prefix := max(0, min(cut-dataStart, len(body)))
		estimate, _ := modelUsageEstimateV1(uint64(prefix), 0)
		if !completion.Estimated || completion.Counts.Requests != 1 || completion.Counts.TotalTokens != estimate {
			t.Fatalf("upload cut%d charged inspected/read-ahead/framing/padding bytes", cut)
		}
		scope.close()
	}
}
