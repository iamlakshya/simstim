package egress

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

type testEnv struct {
	supSess *link.Session
	ctlW    *proto.Writer
	ctlR    *bufio.Reader
	srv     *Server

	mu       sync.Mutex
	audits   []AuditEntry
	events   []string
	ended    []OnceEndEvent
	expiries []RuleExpiryEvent
	nextID   uint64
	waits    map[uint64]chan proto.Response
}

func startTestServer(t *testing.T, d *Dialer) *testEnv {
	t.Helper()
	return startTestServerWithClock(t, d, nil)
}

func startTestServerWithClock(t *testing.T, d *Dialer, clock func() time.Time) *testEnv {
	t.Helper()
	c1, c2 := net.Pipe()
	srv, err := NewServer(c2, Options{Dialer: d, Now: clock, OriginRoots: x509.NewCertPool()})
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()

	supSess := link.New(c1, link.Supervisor, LinkOptions())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctlSt, err := supSess.Open(ctx, link.KindCtl, nil)
	if err != nil {
		t.Fatal(err)
	}
	env := &testEnv{
		supSess: supSess,
		ctlW:    proto.NewWriter(ctlSt),
		ctlR:    bufio.NewReader(ctlSt),
		srv:     srv,
		waits:   map[uint64]chan proto.Response{},
	}
	go env.readCtl()
	t.Cleanup(func() {
		_ = supSess.Close()
		_ = srv.Close()
	})
	return env
}

func (e *testEnv) readCtl() {
	for {
		f, err := proto.Read(e.ctlR)
		if err != nil {
			return
		}
		switch f.Type {
		case proto.TResponse:
			var resp proto.Response
			if json.Unmarshal(f.Payload, &resp) == nil {
				e.mu.Lock()
				ch := e.waits[resp.ID]
				e.mu.Unlock()
				if ch != nil {
					ch <- resp
				}
			}
		case proto.TEvent:
			var ev CtlEvent
			if json.Unmarshal(f.Payload, &ev) != nil {
				continue
			}
			e.mu.Lock()
			e.events = append(e.events, ev.Event)
			switch {
			case ev.Event == EventAudit && ev.Audit != nil:
				e.audits = append(e.audits, *ev.Audit)
			case ev.Event == EventOnceEnd && ev.OnceEnd != nil:
				e.ended = append(e.ended, *ev.OnceEnd)
			case ev.Event == EventRuleExpiry && ev.RuleExpiry != nil:
				e.expiries = append(e.expiries, *ev.RuleExpiry)
			}
			e.mu.Unlock()
		default:
		}
	}
}

func (e *testEnv) call(t *testing.T, op string, args any) {
	t.Helper()
	b, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	ch := make(chan proto.Response, 1)
	e.mu.Lock()
	e.nextID++
	id := e.nextID
	e.waits[id] = ch
	e.mu.Unlock()
	if err := e.ctlW.WriteJSON(proto.TRequest, proto.Request{ID: id, Op: op, Args: b}); err != nil {
		t.Fatal(err)
	}
	select {
	case resp := <-ch:
		if !resp.OK {
			t.Fatalf("%s failed: %+v", op, resp.Error)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", op)
	}
}

func (e *testEnv) waitAudits(t *testing.T, want int) []AuditEntry {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		e.mu.Lock()
		if len(e.audits) >= want {
			out := append([]AuditEntry(nil), e.audits...)
			e.mu.Unlock()
			return out
		}
		e.mu.Unlock()
		time.Sleep(15 * time.Millisecond)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	t.Fatalf("got %d audits, want at least %d: %+v", len(e.audits), want, e.audits)
	return nil
}

func TestServerBalanced403ECHSmugglingAndRevocation(t *testing.T) {
	var upstreamHits atomic.Int32
	originCA, err := newSessionAuthority(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	originLeaf, err := originCA.certificate("registry.npmjs.org", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok:" + r.URL.Path))
	}))
	ts.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{*originLeaf}, NextProtos: []string{"http/1.1"}}
	ts.StartTLS()
	defer ts.Close()
	tsAddr := ts.Listener.Addr().String()

	d := &Dialer{
		Lookup: func(_ context.Context, _ string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("203.0.113.10")}, nil
		},
		DialTCP: func(ctx context.Context, _ netip.AddrPort) (net.Conn, error) {
			var nd net.Dialer
			return nd.DialContext(ctx, "tcp", tsAddr)
		},
	}
	env := startTestServer(t, d)
	originRoots := x509.NewCertPool()
	originRoots.AddCert(originCA.cert)
	env.srv.transport.TLSClientConfig.RootCAs = originRoots

	// 1. Before CtlInit, opening a stream fails closed immediately.
	ctx := context.Background()
	preSt, err := env.supSess.Open(ctx, link.KindEgress, StreamHeader{Via: "explicit", Host: "registry.npmjs.org", Port: 443})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(preSt, "CONNECT registry.npmjs.org:443 HTTP/1.1\r\nHost: registry.npmjs.org:443\r\n\r\n")
	preBytes, _ := io.ReadAll(preSt)
	_ = preSt.Close()
	if len(preBytes) != 0 {
		t.Fatalf("stream before init returned %q, want closed", preBytes)
	}

	// 2. Initialise in balanced preset with an exposed secret.
	pol, _, err := MergeLayers(nil)
	if err != nil {
		t.Fatal(err)
	}
	pol.Unknown = api.UnknownDeny
	env.call(t, CtlInit, InitArgs{
		SessionID:  "abcd2345",
		SessionRef: "work-box",
		Agent:      "shell",
		Policy:     pol,
		Secrets:    map[string]string{"NPM_TOKEN": "npm_super_secret_val"},
	})

	// 3. Allowed balanced host (registry.npmjs.org:443) splices TLS cleanly.
	st1, err := env.supSess.Open(ctx, link.KindEgress, StreamHeader{Via: "explicit", Host: "registry.npmjs.org", Port: 443})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(st1, "CONNECT registry.npmjs.org:443 HTTP/1.1\r\nHost: registry.npmjs.org:443\r\n\r\n")
	br1 := bufio.NewReader(st1)
	resp1, err := http.ReadResponse(br1, nil)
	if err != nil || resp1.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT registry.npmjs.org = %v, %v", resp1, err)
	}
	_ = resp1.Body.Close()
	tlsConn := tls.Client(st1, &tls.Config{ServerName: "registry.npmjs.org", InsecureSkipVerify: true})
	_, _ = io.WriteString(tlsConn, "GET /pkg HTTP/1.1\r\nHost: registry.npmjs.org\r\nConnection: close\r\n\r\n")
	tlsResp, err := http.ReadResponse(bufio.NewReader(tlsConn), nil)
	if err != nil {
		t.Fatalf("TLS response read failed: %v", err)
	}
	gotBody, err := io.ReadAll(tlsResp.Body)
	_ = tlsResp.Body.Close()
	if err != nil || tlsResp.StatusCode != http.StatusOK || string(gotBody) != "ok:/pkg" {
		t.Fatalf("TLS response = status %d body %q, %v", tlsResp.StatusCode, gotBody, err)
	}
	_ = tlsConn.Close()

	// 4. Disallowed host (example.com:443) gets 403 with exact grant command.
	st2, err := env.supSess.Open(ctx, link.KindEgress, StreamHeader{Via: "explicit", Host: "example.com", Port: 443})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(st2, "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n")
	resp2, err := http.ReadResponse(bufio.NewReader(st2), nil)
	if err != nil {
		t.Fatal(err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	_ = resp2.Body.Close()
	_ = st2.Close()
	wantGrant := brand.Name + ": example.com is not allowed in work-box. Try: " + brand.Name + " policy allow example.com --in work-box\n"
	if resp2.StatusCode != http.StatusForbidden || resp2.Header.Get("X-Should-Retry") != "false" || string(body2) != wantGrant {
		t.Fatalf("403 reply = status %d retry=%q body=%q, want %q", resp2.StatusCode, resp2.Header.Get("X-Should-Retry"), body2, wantGrant)
	}

	// 5. ECH ClientHello on an allowed host is refused before reaching upstream.
	hitsBefore := upstreamHits.Load()
	stECH, err := env.supSess.Open(ctx, link.KindEgress, StreamHeader{Via: "explicit", Host: "registry.npmjs.org", Port: 443})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(stECH, "CONNECT registry.npmjs.org:443 HTTP/1.1\r\nHost: registry.npmjs.org:443\r\n\r\n")
	if respECH, err := http.ReadResponse(bufio.NewReader(stECH), nil); err == nil {
		_ = respECH.Body.Close()
	}
	baseCH := captureClientHello(t, &tls.Config{ServerName: "registry.npmjs.org", InsecureSkipVerify: true})
	echCH := appendExtensionOnRecord(t, baseCH, 0xfe0d, []byte{0x00, 0x01, 0x02})
	_, _ = stECH.Write(echCH)
	echReply, _ := io.ReadAll(stECH)
	_ = stECH.Close()
	if len(echReply) != 0 || upstreamHits.Load() != hitsBefore {
		t.Fatalf("ECH flow reached upstream or returned data: reply=%x hits=%d->%d", echReply, hitsBefore, upstreamHits.Load())
	}

	// 6. Smuggling attempt (duplicate Host header) gets 400 Bad Request.
	stSmug, err := env.supSess.Open(ctx, link.KindEgress, StreamHeader{Via: "explicit", Host: "registry.npmjs.org", Port: 443})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(stSmug, "CONNECT registry.npmjs.org:443 HTTP/1.1\r\nHost: registry.npmjs.org:443\r\nHost: evil.com:443\r\n\r\n")
	respSmug, err := http.ReadResponse(bufio.NewReader(stSmug), nil)
	if err != nil || respSmug.StatusCode != http.StatusBadRequest {
		t.Fatalf("duplicate Host header = %v, %v", respSmug, err)
	}
	_ = respSmug.Body.Close()
	_ = stSmug.Close()

	// 7. Plain HTTP denied path containing secret scrubs secret in audit log.
	stHTTP, err := env.supSess.Open(ctx, link.KindEgress, StreamHeader{Via: "explicit", Host: "example.com", Port: 80})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(stHTTP, "GET http://example.com/tokens/npm_super_secret_val/use?x=1 HTTP/1.1\r\nHost: example.com\r\nConnection: close\r\n\r\n")
	_, _ = io.ReadAll(stHTTP)
	_ = stHTTP.Close()

	audits := env.waitAudits(t, 5)
	if audits[2].Rule != "tls:ech" || audits[2].Decision != "deny" {
		t.Errorf("ECH audit = %+v, want tls:ech deny", audits[2])
	}
	if audits[4].Path != "/tokens/[NPM_TOKEN]/use" || strings.Contains(audits[4].Path, "npm_super_secret_val") {
		t.Errorf("secret scrub audit = %+v", audits[4])
	}

	// 8. Live policy revocation closes an already-open tunnel immediately.
	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echoLn.Close()
	go func() {
		c, err := echoLn.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = io.Copy(c, c)
	}()
	d.DialTCP = func(ctx context.Context, _ netip.AddrPort) (net.Conn, error) {
		var nd net.Dialer
		return nd.DialContext(ctx, "tcp", echoLn.Addr().String())
	}
	polAllow, _, err := MergeLayers([]api.Layer{{
		Kind:  api.LayerSession,
		Rules: []api.Rule{{Allow: true, Door: api.DoorNet, Target: "live.example.com:8443"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	env.call(t, CtlPolicyUpdate, PolicyUpdateArgs{Policy: polAllow})

	stLive, err := env.supSess.Open(ctx, link.KindEgress, StreamHeader{Via: "explicit", Host: "live.example.com", Port: 8443})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(stLive, "CONNECT live.example.com:8443 HTTP/1.1\r\nHost: live.example.com:8443\r\n\r\n")
	liveBR := bufio.NewReader(stLive)
	respLive, err := http.ReadResponse(liveBR, nil)
	if err != nil || respLive.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT live.example.com:8443 = %v, %v", respLive, err)
	}
	_ = respLive.Body.Close()
	_, _ = stLive.Write([]byte("ping\n"))
	line, err := liveBR.ReadString('\n')
	if err != nil || line != "ping\n" {
		t.Fatalf("live tunnel echo = %q, %v", line, err)
	}

	// Revoke live.example.com:8443 -> active flow must close promptly!
	env.call(t, CtlPolicyUpdate, PolicyUpdateArgs{Policy: pol})
	_ = stLive.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, err = liveBR.ReadByte()
	if err == nil {
		t.Fatal("open tunnel stayed readable after its rule was removed")
	}
	_ = stLive.Close()
}

func TestEgressChildUndumpable(t *testing.T) {
	if os.Getenv("TEST_CHILD_UNDUMPABLE") == "1" {
		if err := setUndumpable(); err != nil {
			t.Fatalf("setUndumpable: %v", err)
		}
		d, err := Dumpable()
		if err != nil || d != 0 {
			t.Fatalf("Dumpable() = %d, %v; want 0, nil", d, err)
		}
		return
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(context.Background(), exe, "-test.run=^TestEgressChildUndumpable$")
	cmd.Env = append(os.Environ(), "TEST_CHILD_UNDUMPABLE=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child undumpable check failed (%v):\n%s", err, out)
	}
}
