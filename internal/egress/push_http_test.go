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
	"reflect"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
)

func newPushHTTPFixture(t *testing.T, policy api.Policy, body []byte) *modelHTTPFixture {
	t.Helper()
	f := newModelHTTPFixture(t, "shell", policy, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	ca, err := newSessionAuthority(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := ca.certificate("github.com", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	config := &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{*leaf}}
	roots := x509.NewCertPool()
	roots.AddCert(ca.cert)
	f.env.srv.transport.TLSClientConfig.RootCAs = roots
	f.env.srv.dialer.DialTCP = func(_ context.Context, address netip.AddrPort) (net.Conn, error) {
		if address != netip.MustParseAddrPort("198.51.100.8:443") {
			t.Error("unchecked push endpoint")
		}
		f.dials.Add(1)
		client, origin := net.Pipe()
		t.Cleanup(func() { _ = client.Close(); _ = origin.Close() })
		go serveHTTPConnection(t.Context(), tls.Server(origin, config), config, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			f.requests.Add(1)
			actual, err := io.ReadAll(r.Body)
			if err != nil || !bytes.Equal(actual, body) {
				http.Error(w, "push bytes changed", 500)
				return
			}
			w.WriteHeader(http.StatusOK)
		}), nil)
		return client, nil
	}
	return f
}

func pushFixtureRequest(t *testing.T, f *modelHTTPFixture, method, path string, body []byte) (*httptest.ResponseRecorder, AuditEntry) {
	t.Helper()
	req := httptest.NewRequest(method, "https://github.com"+path, bytes.NewReader(body)).WithContext(t.Context())
	req.Header.Set("Content-Type", "application/x-git-receive-pack-request")
	w := httptest.NewRecorder()
	audit := f.env.srv.proxyHTTPRequest(w, req, StreamHeader{Via: "explicit", Host: "github.com", Port: 443}, "https", nil)
	return w, audit.get()
}

func TestPushActualHTTPCallerChecksEveryCommandBeforeAnyOriginIO(t *testing.T) {
	prefix := "refs/heads/" + brand.Name + "/model234567/"
	valid := append(receiveTestPacket(receiveTestCommand(prefix+"fix", 40, false)+"\x00report-status"), []byte("0000PACKauthored-tail")...)
	for _, body := range [][]byte{
		append(receiveTestPacket(receiveTestCommand("refs/heads/main", 40, false)+"\x00report-status"), []byte("0000PACK")...),
		append(append(receiveTestPacket(receiveTestCommand(prefix+"fix", 40, false)+"\x00report-status"), receiveTestPacket(receiveTestCommand("refs/tags/release", 40, false))...), []byte("0000PACK")...),
		[]byte("ffff"),
	} {
		f := newPushHTTPFixture(t, api.Policy{Preset: api.PresetBalanced, Rules: []api.Rule{pushTestRule()}}, body)
		before := snapshotDNSLedger(f.env.srv.currentEngine())
		response, audit := pushFixtureRequest(t, f, "POST", "/org/repo.git/git-receive-pack", body)
		if response.Code != http.StatusForbidden || f.lookups.Load() != 0 || f.dials.Load() != 0 || f.requests.Load() != 0 || audit.Decision != "deny" {
			t.Fatal("unchecked receive-pack prefix reached an origin")
		}
		if !reflect.DeepEqual(before, snapshotDNSLedger(f.env.srv.currentEngine())) {
			t.Fatal("invalid prefix transiently spent a push or net lease")
		}
	}
	f := newPushHTTPFixture(t, api.Policy{Preset: api.PresetLocked, Rules: []api.Rule{pushTestRule()}}, valid)
	response, _ := pushFixtureRequest(t, f, "POST", "/org/repo.git/git-receive-pack", valid)
	if response.Code != http.StatusOK || f.requests.Load() != 1 {
		t.Fatalf("valid original push bytes did not cross the checked transport: %d", response.Code)
	}
	second, _ := pushFixtureRequest(t, f, "POST", "/org/repo.git/git-receive-pack", valid)
	if second.Code != http.StatusForbidden || f.requests.Load() != 1 {
		t.Fatal("a second actual POST reused the spent push lease")
	}
}

func TestPushActualDiscoveryLeavesSolePOSTLeaseAvailable(t *testing.T) {
	f := newPushHTTPFixture(t, api.Policy{Preset: api.PresetLocked, Rules: []api.Rule{pushTestRule()}}, nil)
	before := snapshotDNSLedger(f.env.srv.currentEngine())
	response, _ := pushFixtureRequest(t, f, "GET", "/org/repo.git/info/refs?service=git-receive-pack", nil)
	if response.Code != http.StatusOK || f.requests.Load() != 1 || !reflect.DeepEqual(before, snapshotDNSLedger(f.env.srv.currentEngine())) {
		t.Fatal("actual discovery reserved or spent the push lease")
	}
	if !f.env.srv.currentEngine().evaluateHTTP(pushTestRequest(false), nil, evalPrecheck).Allow {
		t.Fatal("discovery prevented its subsequent POST")
	}
}
