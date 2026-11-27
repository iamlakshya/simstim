package egress

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"golang.org/x/net/http2"
)

func TestPhysicalOriginPrototypeKeepsVerifiedH2AndJoinsEarlyUploadWrites(t *testing.T) {
	for _, early := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "early-answer"}[early], func(t *testing.T) {
			body := `{"model":"model-a","input":"original"}`
			if early {
				body = `{"model":"model-a","input":"` + strings.Repeat("x", 2<<20) + `"}`
			}
			server := newMemberServer(t, InitArgs{SessionID: "origin234", Agent: "codex", Policy: api.Policy{Preset: api.PresetLocked}})
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
			server.transport.TLSClientConfig.RootCAs = roots
			var dials atomic.Int64
			originResult := make(chan error, 1)
			server.dialer.Lookup = func(context.Context, string) ([]netip.Addr, error) {
				return []netip.Addr{netip.MustParseAddr("198.51.100.8")}, nil
			}
			server.dialer.DialTCP = func(context.Context, netip.AddrPort) (net.Conn, error) {
				dials.Add(1)
				client, peer := net.Pipe()
				t.Cleanup(func() { _ = client.Close(); _ = peer.Close() })
				config := &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{*certificate}, NextProtos: []string{http2.NextProtoTLS}}
				go serveHTTPConnection(t.Context(), tls.Server(peer, config), config, http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
					if request.ProtoMajor != 2 || request.TLS == nil || request.TLS.NegotiatedProtocol != http2.NextProtoTLS {
						originResult <- errPhysicalH2Binding
						return
					}
					if !early {
						actual, err := io.ReadAll(request.Body)
						if err != nil || string(actual) != body {
							originResult <- errPhysicalH2Binding
							return
						}
					}
					w.Header().Set("Content-Type", "application/json")
					_, err := io.WriteString(w, `{"answer":"owned"}`)
					originResult <- err
				}), nil)
				return client, nil
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			request := httptest.NewRequest("POST", "https://api.openai.com:443/v1/responses", bytes.NewBufferString(body)).WithContext(ctx)
			canonical, err := canonicalHTTPRequest(request, StreamHeader{Via: "explicit", Host: "api.openai.com", Port: 443}, "https")
			if err != nil {
				t.Fatal(err)
			}
			scope := &originRequest{s: server, request: canonical, scheme: "https", permit: server.currentEngine().NewPermit(), ctx: ctx, cancel: cancel}
			if !scope.track() {
				t.Fatal("original scope admission failed")
			}
			defer scope.close()
			model, refusal := server.prepareModelHTTPRequest(scope, request)
			if refusal != "" || model == nil || model.usage == nil {
				t.Fatal("original model policy/body guard refused the fixture", refusal)
			}
			request.RequestURI = ""
			request = request.WithContext(context.WithValue(ctx, originRequestKey{}, scope))
			response, err := (requestHTTPTransport{server: server, scope: scope}).roundTripPhysicalOriginH2Prototype(request)
			if err != nil {
				t.Fatal("public uncached H2 view lost the original checked TLS connection", err)
			}
			answer, err := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if err != nil || string(answer) != `{"answer":"owned"}` || response.ProtoMajor != 2 || response.TLS == nil || len(response.TLS.VerifiedChains) == 0 || dials.Load() != 1 {
				t.Fatal("prototype downgraded H2, redialled or changed the verified answer", err)
			}
			if err := <-originResult; err != nil {
				t.Fatal(err)
			}
			model.usage.mu.Lock()
			actual := model.usage.requestBytes
			model.usage.mu.Unlock()
			if !early && actual != uint64(len(body)) || early && actual >= uint64(len(body)) {
				t.Fatal("early-response upload count still used the full inspected body")
			}
			completion := model.usage.FinishV1(false)
			estimate, _ := modelUsageEstimateV1(actual, 0) // no answer bytes were forwarded to a guest in this upload-only fixture
			if !completion.Estimated || completion.Counts.TotalTokens != estimate || completion.Counts.Requests != 1 {
				t.Fatal("upload finalisation did not use its joined physical request prefix")
			}
		})
	}
}
