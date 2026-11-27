package guest

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

func newProxyOriginTLSServer(t *testing.T, host string, h http.Handler) (*httptest.Server, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: host},
		DNSNames:              []string{host},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)

	ts := httptest.NewUnstartedServer(h)
	ts.TLS = &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
	}
	ts.StartTLS()
	return ts, roots
}

func TestGuestProxyEndToEndWithEgressAndCurl(t *testing.T) {
	ts, originRoots := newProxyOriginTLSServer(t, "registry.npmjs.org", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("npm-ok:" + r.URL.Path))
	}))
	defer ts.Close()
	tsAddr := ts.Listener.Addr().String()

	// Guest link pair (supervisor <-> guest) and egress link pair (supervisor <-> egress).
	gSupConn, gGuestFile, err := SocketPair()
	if err != nil {
		t.Fatal(err)
	}
	gGuestConn, err := net.FileConn(gGuestFile)
	_ = gGuestFile.Close()
	if err != nil {
		t.Fatal(err)
	}
	gSup := link.New(gSupConn, link.Supervisor, link.Options{})
	gGuest := link.New(gGuestConn, link.Guest, link.Options{})

	eSupConn, eEgFile, err := SocketPair()
	if err != nil {
		t.Fatal(err)
	}
	eEgConn, err := net.FileConn(eEgFile)
	_ = eEgFile.Close()
	if err != nil {
		t.Fatal(err)
	}
	dialer := &egress.Dialer{
		Lookup: func(_ context.Context, h string) ([]netip.Addr, error) {
			if h == "meta.example.com" {
				return []netip.Addr{netip.MustParseAddr("169.254.169.254")}, nil
			}
			return []netip.Addr{netip.MustParseAddr("203.0.113.10")}, nil
		},
		DialTCP: func(ctx context.Context, _ netip.AddrPort) (net.Conn, error) {
			var nd net.Dialer
			return nd.DialContext(ctx, "tcp", tsAddr)
		},
	}
	egSrv, err := egress.NewServer(eEgConn, egress.Options{Dialer: dialer, OriginRoots: originRoots})
	if err != nil {
		t.Fatal(err)
	}
	go egSrv.Serve()
	eSup := link.New(eSupConn, link.Supervisor, egress.LinkOptions())

	// Forward guest KindEgress streams across the supervisor to egress.
	go func() {
		for {
			st, err := gSup.Accept(context.Background())
			if err != nil {
				return
			}
			go func(gst *link.Stream) {
				defer gst.Close()
				var raw json.RawMessage
				if gst.Header(&raw) != nil {
					return
				}
				hdr, err := egress.ParseStreamHeader(raw)
				if err != nil {
					return
				}
				hdr.Kind = ""
				est, err := eSup.Open(context.Background(), link.KindEgress, hdr)
				if err != nil {
					return
				}
				defer est.Close()
				done := make(chan struct{}, 2)
				go func() { _, _ = io.Copy(est, gst); _ = est.CloseWrite(); done <- struct{}{} }()
				go func() { _, _ = io.Copy(gst, est); _ = gst.CloseWrite(); done <- struct{}{} }()
				<-done
				<-done
			}(st)
		}
	}()

	// Initialise egress with balanced policy.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctlSt, err := eSup.Open(ctx, link.KindCtl, nil)
	if err != nil {
		t.Fatal(err)
	}
	pol, _, err := egress.MergeLayers([]api.Layer{{Kind: api.LayerSession, Unknown: api.UnknownDeny}})
	if err != nil {
		t.Fatal(err)
	}
	initBytes, _ := json.Marshal(egress.InitArgs{SessionID: "abcd2345", SessionRef: "dev-box", Agent: "claude", Policy: pol})
	if err := proto.NewWriter(ctlSt).WriteJSON(proto.TRequest, proto.Request{ID: 1, Op: egress.CtlInit, Args: initBytes}); err != nil {
		t.Fatal(err)
	}
	ctlReader := bufio.NewReader(ctlSt)
	for {
		f, err := proto.Read(ctlReader)
		if err != nil {
			t.Fatal(err)
		}
		if f.Type == proto.TEvent {
			continue
		}
		if f.Type != proto.TResponse {
			t.Fatalf("CtlInit reply = %v, want response", f)
		}
		break
	}

	px, err := StartProxy(gGuest, "http://127.0.0.1:0", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = px.Close()
		_ = gGuest.Close()
		_ = gSup.Close()
		_ = eSup.Close()
		_ = egSrv.Close()
	})

	proxyURL, _ := url.Parse(px.URL())
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	// Allowed in balanced: registry.npmjs.org -> 200 OK.
	respOK, err := client.Get("https://registry.npmjs.org/lodash")
	if err != nil {
		t.Fatalf("GET https://registry.npmjs.org/lodash failed: %v", err)
	}
	bodyOK, _ := io.ReadAll(respOK.Body)
	_ = respOK.Body.Close()
	if respOK.StatusCode != http.StatusOK || string(bodyOK) != "npm-ok:/lodash" {
		t.Fatalf("registry.npmjs.org = %d %q", respOK.StatusCode, bodyOK)
	}

	// Direct CONNECT check verifies the exact one-line 403 body of spec 7.2.
	rawConn, err := net.DialTimeout("tcp", proxyURL.Host, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(rawConn, "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n")
	rawResp, err := http.ReadResponse(bufio.NewReader(rawConn), &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatal(err)
	}
	connectBody, _ := io.ReadAll(rawResp.Body)
	_ = rawResp.Body.Close()
	_ = rawConn.Close()
	wantLine := brand.Name + ": example.com is not allowed in dev-box. Try: " + brand.Name + " policy allow example.com --in dev-box\n"
	if rawResp.StatusCode != http.StatusForbidden || string(connectBody) != wantLine {
		t.Fatalf("CONNECT 403 = %d %q, want %q", rawResp.StatusCode, connectBody, wantLine)
	}

	// Denied in balanced: example.com -> proxy 403 error containing grant hint.
	if curlBin, err := exec.LookPath("curl"); err == nil {
		cmdOK := exec.CommandContext(context.Background(), curlBin, "-sk", "--proxy", px.URL(), "https://registry.npmjs.org/curl-check")
		outOK, err := cmdOK.CombinedOutput()
		if err != nil || !strings.Contains(string(outOK), "npm-ok:/curl-check") {
			t.Fatalf("curl registry.npmjs.org failed (%v): %s", err, outOK)
		}

		cmdDeny := exec.CommandContext(context.Background(), curlBin, "-si", "--proxy", px.URL(), "https://example.com/")
		outDeny, _ := cmdDeny.CombinedOutput()
		wantHint := "Try: " + brand.Name + " policy allow example.com --in dev-box"
		if !strings.Contains(string(outDeny), "403 Forbidden") || !strings.Contains(string(outDeny), wantHint) {
			t.Fatalf("curl example.com output missing 403 or grant command:\n%s", outDeny)
		}

		cmdHTTPDeny := exec.CommandContext(context.Background(), curlBin, "-si", "--proxy", px.URL(), "http://example.com/")
		outHTTPDeny, _ := cmdHTTPDeny.CombinedOutput()
		wantHTTPHint := "Try: " + brand.Name + " policy allow example.com:80 --in dev-box"
		if !strings.Contains(string(outHTTPDeny), "403 Forbidden") || !strings.Contains(string(outHTTPDeny), wantHTTPHint) {
			t.Fatalf("curl http://example.com output missing 403 or grant command:\n%s", outHTTPDeny)
		}

		cmdMeta := exec.CommandContext(context.Background(), curlBin, "-si", "--proxy", px.URL(), "https://169.254.169.254/latest/meta-data/")
		outMeta, _ := cmdMeta.CombinedOutput()
		if !strings.Contains(string(outMeta), "403 Forbidden") {
			t.Fatalf("curl 169.254.169.254 missing 403:\n%s", outMeta)
		}

		cmdMetaDNS := exec.CommandContext(context.Background(), curlBin, "-si", "--proxy", px.URL(), "https://meta.example.com/")
		outMetaDNS, _ := cmdMetaDNS.CombinedOutput()
		if !strings.Contains(string(outMetaDNS), "403 Forbidden") {
			t.Fatalf("curl meta.example.com (resolving to metadata) missing 403:\n%s", outMetaDNS)
		}
	}
}
