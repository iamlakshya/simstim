package egress

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

type judgeAfterHandshakeCut struct {
	net.Conn
	cut        *atomic.Bool
	handshaken <-chan struct{}
}

func (c *judgeAfterHandshakeCut) Write(payload []byte) (int, error) {
	if c.cut.Load() {
		return 0, io.ErrClosedPipe
	}
	n, err := c.Conn.Write(payload)
	// The fixture negotiates TLS1.3. Its first client application-data
	// record is the encrypted Finished flight. Keep that native Write
	// admitted until the peer's real handshake completion arms the cut.
	// This is public TLS record framing, not a private Go state hook.
	for offset := 0; offset+5 <= len(payload); {
		length := int(binary.BigEndian.Uint16(payload[offset+3 : offset+5]))
		if payload[offset] == 23 {
			<-c.handshaken
			break
		}
		if length > len(payload)-offset-5 {
			break
		}
		offset += 5 + length
	}
	return n, err
}

type physicalJudgeCompleteProof struct {
	dials      atomic.Int64
	handshakes atomic.Int64
}

func physicalJudgeCompleteFixture(t *testing.T, handler http.Handler, cutAfterHandshake bool) (*Server, *judgeModelCapability, BoundInitArgs, BoundInitResult, <-chan struct{}, *physicalJudgeCompleteProof) {
	t.Helper()
	args := trustedRoleArgs(t, false)
	server, initial := newBoundRegistryServer(t, args)
	endpoint, request, _, err := judgeEndpoint(*args.Initial.Policy.Classifier.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	capability := &judgeModelCapability{server: server, endpoint: endpoint, request: request, settings: args.Initial.Policy.Classifier, key: "KEY"}
	ca, err := newSessionAuthority(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := ca.certificate(request.Host, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca.cert)
	capability.testOriginRoots = roots
	closed := make(chan struct{})
	proof := new(physicalJudgeCompleteProof)
	capability.testOriginDial = func(ctx context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" || address != endpoint.Host || proof.dials.Add(1) != 1 {
			return nil, errJudgeTransport
		}
		local, peer := net.Pipe()
		_ = local.SetDeadline(time.Now().Add(5 * time.Second))
		_ = peer.SetDeadline(time.Now().Add(5 * time.Second))
		t.Cleanup(func() { _ = local.Close(); _ = peer.Close() })
		cut := new(atomic.Bool)
		handshaken := make(chan struct{})
		handshakeDone := sync.OnceFunc(func() { close(handshaken) })
		go func() {
			defer close(closed)
			defer peer.Close()
			defer handshakeDone()
			config := &tls.Config{MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13, SessionTicketsDisabled: true, Certificates: []tls.Certificate{*certificate}}
			actual := tls.Server(peer, config)
			if err := actual.HandshakeContext(ctx); err != nil {
				return
			}
			proof.handshakes.Add(1)
			if cutAfterHandshake {
				cut.Store(true)
			}
			handshakeDone()
			if cutAfterHandshake {
				var probe [1]byte
				if n, _ := actual.Read(probe[:]); n != 0 {
					t.Error("private header/request bytes crossed the original post-handshake cut")
				}
				return
			}
			serveHTTPConnection(ctx, actual, config, handler, nil)
		}()
		if cutAfterHandshake {
			return &judgeAfterHandshakeCut{Conn: local, cut: cut, handshaken: handshaken}, nil
		}
		return local, nil
	}
	return server, capability, args, initial, closed, proof
}

func TestActualPrivateJudgeCompleteTraversesOriginalRoleGuardAndVerifiedTLSWriter(t *testing.T) {
	var requests atomic.Int64
	server, capability, _, _, closed, proof := physicalJudgeCompleteFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != `{}` || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer ABCD0000" || r.TLS == nil || !r.TLS.HandshakeComplete || r.ProtoMajor != 1 {
			t.Error("original private Judge request, role header, payload or TLS changed")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[],"usage":{"total_tokens":5}}`)
	}), false)
	if capability.testRoundTrip != nil {
		t.Fatal("actual Complete fixture bypassed its guarded native transport")
	}
	answer, err := capability.Complete(judgeSourceContext(t, server), []byte(`{}`))
	if err != nil || string(answer) != `{"choices":[],"usage":{"total_tokens":5}}` {
		t.Fatal("actual private Judge answer changed", err)
	}
	waitHTTPProgress(t, closed, "the original private Judge TLS owner to close")
	usage := server.modelUsageSnapshot()
	if proof.dials.Load() != 1 || proof.handshakes.Load() != 1 || requests.Load() != 1 || len(server.flows.snapshot()) != 0 || usage.Total.Requests != 1 || usage.Total.TotalTokens != 5 || usage.Total.EstimatedRequests != 0 || server.currentEngine().Decide(Request{Host: capability.request.Host, Port: 443}).Allow {
		t.Fatal("private Judge escaped its original meter, Source lifetime or network authority")
	}
}

func TestActualPrivateJudgeCompleteCountsNoUnwrittenBodyAtOriginalVerifiedTLSCut(t *testing.T) {
	server, capability, _, _, closed, proof := physicalJudgeCompleteFixture(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("unwritten private Judge request reached an HTTP handler")
	}), true)
	payload := []byte(`{"input":"` + strings.Repeat("x", 16<<10) + `"}`)
	if _, err := capability.Complete(judgeSourceContext(t, server), payload); err == nil {
		t.Fatal("post-handshake original native cut reported an answer")
	}
	waitHTTPProgress(t, closed, "the actual cut private Judge TLS connection to close")
	usage := server.modelUsageSnapshot()
	if proof.dials.Load() != 1 || proof.handshakes.Load() != 1 || len(server.flows.snapshot()) != 0 || usage.Total.Requests != 1 || usage.Total.EstimatedRequests != 1 || usage.Total.TotalTokens != 0 {
		t.Fatal("actual Complete charged inspected or unwritten request bytes", usage.Total)
	}
}

func TestActualPrivateJudgeCompleteRejectsRetiredRoleSourceBeforeNativeDial(t *testing.T) {
	server, capability, args, initial, _, proof := physicalJudgeCompleteFixture(t, http.NotFoundHandler(), false)
	ctx := judgeSourceContext(t, server)
	_, err := server.armControl(memberJSON(t, ArmArgs{Version: MemberRegistryV2, Generation: initial.Generation, TrustedUses: args.TrustedUses, ProtectedTrustedSecretValues: map[string]api.Secret{"KEY": "WXYZ1111"}}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := capability.Complete(ctx, []byte(`{}`)); err == nil {
		t.Fatal("retired private role source reached Complete")
	}
	if proof.dials.Load() != 0 || len(server.flows.snapshot()) != 0 || server.modelUsageSnapshot().Total.Requests != 0 {
		t.Fatal("retired source reached the native origin or created a model turn")
	}
}

func TestActualPrivateJudgeSourceArmJoinsItsOriginalTLSResponseBeforeReplacement(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan struct{})
	server, capability, args, initial, closed, _ := physicalJudgeCompleteFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"usage":`)
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
		close(cancelled)
	}), false)
	done := make(chan error, 1)
	ctx := judgeSourceContext(t, server)
	go func() { _, err := capability.Complete(ctx, []byte(`{}`)); done <- err }()
	waitHTTPProgress(t, started, "the original private role's actual TLS response prefix")
	_, err := server.armControl(memberJSON(t, ArmArgs{Version: MemberRegistryV2, Generation: initial.Generation, TrustedUses: args.TrustedUses, ProtectedTrustedSecretValues: map[string]api.Secret{"KEY": "WXYZ1111"}}))
	if err != nil {
		t.Fatal(err)
	}
	waitHTTPProgress(t, cancelled, "the original response context to end before trusted-role replacement")
	waitHTTPProgress(t, closed, "the original actual TLS reader/writer owner to join")
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("source-retired private Judge call succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("source arm released an unfinished private Judge call")
	}
	usage := server.modelUsageSnapshot()
	if len(server.flows.snapshot()) != 0 || usage.Total.Requests != 1 || usage.Total.EstimatedRequests != 1 {
		t.Fatal("actual source retirement lost its unfinished request or private owner")
	}
}
