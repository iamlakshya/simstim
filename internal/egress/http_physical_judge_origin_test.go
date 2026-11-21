package egress

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPhysicalJudgeOriginKeepsItsOriginalDialAndJoinsEarlyAnswer(t *testing.T) {
	for _, h2 := range []bool{false, true} {
		for _, early := range []bool{false, true} {
			body := `{"model":"judge","messages":[]}`
			if early {
				body = `{"model":"judge","messages":["` + strings.Repeat("x", 2<<20) + `"]}`
			}
			ca, err := newSessionAuthority(time.Now())
			if err != nil {
				t.Fatal(err)
			}
			certificate, err := ca.certificate("judge.example.test", time.Now())
			if err != nil {
				t.Fatal(err)
			}
			roots := x509.NewCertPool()
			roots.AddCert(ca.cert)
			ctx, cancel := context.WithCancel(t.Context())
			origin := newJudgeOrigin()
			t.Cleanup(cancel)
			t.Cleanup(origin.close)
			stop := context.AfterFunc(ctx, origin.close)
			protocols := new(http.Protocols)
			protocols.SetHTTP1(true)
			protocols.SetHTTP2(h2)
			var dials atomic.Int64
			answered := make(chan error, 1)
			template := &http.Transport{Protocols: protocols, TLSHandshakeTimeout: time.Second, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}, DisableKeepAlives: true, DisableCompression: true}
			template.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				if network != "tcp" || address != "judge.example.test:443" || !origin.beginDial() {
					return nil, errJudgeTransport
				}
				defer origin.dials.Done()
				dials.Add(1)
				client, peer := net.Pipe()
				config := &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{*certificate}, NextProtos: []string{"http/1.1"}}
				if h2 {
					config.NextProtos = []string{"h2", "http/1.1"}
				}
				go func() {
					defer peer.Close()
					serveHTTPConnection(ctx, tls.Server(peer, config), config, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if !early {
							actual, err := io.ReadAll(r.Body)
							if err != nil || string(actual) != body {
								answered <- errPhysicalH2Binding
								return
							}
						}
						_, err := io.WriteString(w, `{"usage":{"total_tokens":5}}`)
						answered <- err
					}), nil)
				}()
				return origin.attach(client)
			}
			meter := NewModelUsageMeterV1()
			turn, refusal, err := meter.BeginV1(ModelUsageRequestV1{Wire: "openai-chat-completions", Path: "/v1/chat/completions", Model: "judge", RequestBytes: uint64(len(body))}, 0)
			if err != nil || refusal != nil {
				t.Fatal(err)
			}
			request, err := http.NewRequestWithContext(ctx, "POST", "https://judge.example.test:443/v1/chat/completions", strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			response, client, upload, err := roundTripPhysicalJudgeOrigin(request, template, turn, "judge.example.test", func(ctx context.Context) bool { return ctx.Err() == nil })
			if err != nil {
				t.Fatal("fixed origin physical request failed", err)
			}
			answer, err := io.ReadAll(response.Body)
			_ = response.Body.Close()
			_ = upload.Close()
			_ = client.Close()
			if err != nil || string(answer) != `{"usage":{"total_tokens":5}}` || dials.Load() != 1 || response.TLS == nil || len(response.TLS.VerifiedChains) == 0 {
				t.Fatal("fixed Judge origin, TLS or answer changed", err)
			}
			if err := <-answered; err != nil {
				t.Fatal(err)
			}
			turn.mu.Lock()
			actual := turn.requestBytes
			turn.mu.Unlock()
			if !early && actual != uint64(len(body)) || early && actual >= uint64(len(body)) {
				t.Fatal("Judge request charged the inspected body before physical upload")
			}
			completion := turn.FinishV1(false)
			estimate, _ := modelUsageEstimateV1(actual, 0)
			if !completion.Estimated || completion.Counts.TotalTokens != estimate {
				t.Fatal("Judge did not settle the joined physical prefix")
			}
			cancel()
			origin.close()
			stop()
		}
	}
}
