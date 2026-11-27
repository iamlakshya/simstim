package supervisor

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/alerts"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/guest"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/version"
)

func newEgressOriginTLSServer(t *testing.T, h http.Handler, hosts ...string) (*httptest.Server, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: hosts[0]},
		DNSNames:              hosts,
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
	ts := httptest.NewUnstartedServer(h)
	ts.TLS = &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
	}
	ts.StartTLS()
	return ts, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func startEgressSession(t *testing.T, id, agentKind string, pol api.Policy, secretVal string, upstreamAddr, dumpOut string, originCAPEM ...[]byte) (*Supervisor, string) {
	t.Helper()
	dir := testFolders(t)
	proxyOut := filepath.Join(dir, "proxy.url")
	t0 := time.Now().UTC()

	oldProxy, oldEnv := testPlainProxy, testEgressEnv
	testPlainProxy = "http://127.0.0.1:0"
	testEgressEnv = []string{
		"TEST_EGRESS_UPSTREAM=" + upstreamAddr,
		"TEST_EGRESS_DUMPABLE_OUT=" + dumpOut,
	}
	if len(originCAPEM) > 0 && len(originCAPEM[0]) > 0 {
		caPath := filepath.Join(dir, "origin-ca.pem")
		if err := os.WriteFile(caPath, originCAPEM[0], 0o600); err != nil {
			t.Fatal(err)
		}
		testEgressEnv = append(testEgressEnv, "TEST_EGRESS_ORIGIN_CA="+caPath)
	}
	t.Cleanup(func() {
		testPlainProxy = oldProxy
		testEgressEnv = oldEnv
	})

	env := []string{
		"PATH=/usr/bin:/bin",
		"HOME=" + host.GuestHome,
		brand.Env("HOOK_SOCK") + "=" + host.GuestHookSocket(),
		"TERM=xterm-256color",
		"HTTP_PROXY=" + host.DefaultGuestProxy,
		"HTTPS_PROXY=" + host.DefaultGuestProxy,
	}
	var secretNames []string
	if secretVal != "" {
		secretNames = []string{"API_SECRET"}
	}
	m := &host.Manifest{
		Schema:    1,
		ID:        id,
		Name:      "egress-box",
		CreatedAt: t0,
		Cwd:       dir,
		Agent: host.AgentInfo{
			Kind: agentKind,
			Argv: []string{"/bin/sh", "-c", "printf '%s' \"$HTTPS_PROXY\" > '" + proxyOut + "'; exec sleep 300"},
		},
		Env:     env,
		Term:    host.TermInfo{Term: "xterm-256color", Cols: 80, Rows: 24},
		Status:  host.Status{State: host.StateStarting, Since: t0},
		Wall:    &host.WallRecord{Level: "none", Driver: driverPlain, Version: version.Version, At: t0},
		Policy:  &pol,
		Layers:  []api.Layer{{Kind: api.LayerSession, Preset: pol.Preset, Unknown: pol.Unknown}},
		Secrets: secretNames,
	}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}

	supCh := make(chan *Supervisor, 1)
	testStarted = func(s *Supervisor) { supCh <- s }
	t.Cleanup(func() { testStarted = nil })

	ended := make(chan error, 1)
	go func() { ended <- Run(id) }()

	if secretVal != "" {
		waitFor(t, "the session socket for secrets", func() bool {
			_, err := os.Stat(paths.Socket(id))
			return err == nil
		})
		_, err := host.HolderRequest(id, "secrets", map[string]any{
			"env": map[string]api.Secret{"API_SECRET": api.Secret(secretVal)},
		}, 5*time.Second)
		if err != nil {
			t.Fatalf("deliver secrets: %v", err)
		}
	}

	var sup *Supervisor
	select {
	case sup = <-supCh:
	case err := <-ended:
		t.Fatalf("supervisor exited before starting: %v", err)
	case <-time.After(20 * time.Second):
		t.Fatal("timed out waiting for supervisor start")
	}
	t.Cleanup(func() {
		select {
		case sup.quit <- "test cleanup":
		default:
		}
		select {
		case <-ended:
		case <-time.After(15 * time.Second):
		}
	})

	var proxyURL string
	waitFor(t, "guest proxy URL", func() bool {
		b, err := os.ReadFile(proxyOut)
		proxyURL = strings.TrimSpace(string(b))
		return err == nil && strings.HasPrefix(proxyURL, "http://127.0.0.1:")
	})
	return sup, proxyURL
}

func proxyClient(t *testing.T, rawProxyURL string) *http.Client {
	t.Helper()
	u, err := url.Parse(rawProxyURL)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(u),
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
}

func TestSupervisedEgressLivePolicySecretScrubAndDumpable(t *testing.T) {
	ts, originCA := newEgressOriginTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok:" + r.Host))
	}), "registry.npmjs.org", "api.anthropic.com", "example.com")
	defer ts.Close()

	dumpOut := filepath.Join(t.TempDir(), "dumpable")
	pol, _, err := egress.MergeLayers([]api.Layer{{Kind: api.LayerSession, Unknown: api.UnknownDeny}})
	if err != nil {
		t.Fatal(err)
	}
	secretVal := "fake-secret-for-tests-00000000"
	sup, proxyURL := startEgressSession(t, "eglive23", "claude", pol, secretVal, ts.Listener.Addr().String(), dumpOut, originCA)

	m, err := host.LoadManifest("eglive23")
	if err != nil || !m.Armed {
		t.Fatalf("manifest Armed = %v, err = %v; want Armed=true", m != nil && m.Armed, err)
	}

	hc := proxyClient(t, proxyURL)

	// 1. Balanced preset allows registry.npmjs.org and Claude's Egress host api.anthropic.com
	//    (with a valid Messages API request path under the model guard).
	respPkg, err := hc.Get("https://registry.npmjs.org/v1")
	if err != nil {
		t.Fatalf("GET https://registry.npmjs.org/v1 failed: %v", err)
	}
	_ = respPkg.Body.Close()
	if respPkg.StatusCode != http.StatusOK {
		t.Fatalf("https://registry.npmjs.org/v1 status = %d", respPkg.StatusCode)
	}
	const modelReq = `{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}]}`
	respModel, err := hc.Post("https://api.anthropic.com/v1/messages", "application/json", strings.NewReader(modelReq))
	if err != nil {
		t.Fatalf("POST https://api.anthropic.com/v1/messages failed: %v", err)
	}
	_ = respModel.Body.Close()
	if respModel.StatusCode != http.StatusOK {
		t.Fatalf("https://api.anthropic.com/v1/messages status = %d", respModel.StatusCode)
	}

	if runtime.GOOS == "linux" {
		waitFor(t, "dumpable file", func() bool {
			b, err := os.ReadFile(dumpOut)
			return err == nil && strings.TrimSpace(string(b)) == "0"
		})
	}

	// 2. example.com is denied at first; live policy.add allows it; live policy.rm revokes it.
	if respDeny1, err := hc.Get("https://example.com/"); err == nil || !strings.Contains(err.Error(), "Forbidden") {
		if respDeny1 != nil {
			_ = respDeny1.Body.Close()
		}
		t.Fatalf("initial https://example.com/ err = %v, want 403 Forbidden", err)
	}

	rawRule, err := host.HolderRequest("eglive23", "policy.add", api.PolicyAddArgs{
		Allow:  true,
		Door:   api.DoorNet,
		Target: "example.com",
	}, 5*time.Second)
	if err != nil {
		t.Fatalf("policy.add failed: %v", err)
	}
	var added api.Rule
	if err := json.Unmarshal(rawRule, &added); err != nil || !strings.HasPrefix(added.ID, "g") {
		t.Fatalf("policy.add rule = %+v, %v", added, err)
	}

	respEx, err := hc.Get("https://example.com/")
	if err != nil || respEx.StatusCode != http.StatusOK {
		t.Fatalf("after policy.add, https://example.com/ = %v, %v", respEx, err)
	}
	_ = respEx.Body.Close()

	if _, err := host.HolderRequest("eglive23", "policy.rm", api.PolicyRmArgs{ID: added.ID}, 5*time.Second); err != nil {
		t.Fatalf("policy.rm failed: %v", err)
	}
	if respDeny2, err := hc.Get("https://example.com/"); err == nil || !strings.Contains(err.Error(), "Forbidden") {
		if respDeny2 != nil {
			_ = respDeny2.Body.Close()
		}
		t.Fatalf("after policy.rm, https://example.com/ err = %v, want 403 Forbidden", err)
	}

	// 3. Plain HTTP request whose path holds the exposed secret writes [API_SECRET] in audit.jsonl.
	respSec, err := hc.Get("http://example.com/hook/" + secretVal + "/ping?raw=" + secretVal)
	if err != nil {
		t.Fatalf("plain HTTP via proxy failed: %v", err)
	}
	bodySec, _ := io.ReadAll(respSec.Body)
	_ = respSec.Body.Close()
	if respSec.StatusCode != http.StatusForbidden || !strings.Contains(string(bodySec), "policy allow example.com:80 --in egress-box") {
		t.Fatalf("plain HTTP 403 = %d %q", respSec.StatusCode, bodySec)
	}

	waitFor(t, "scrubbed net audit line", func() bool {
		b, err := os.ReadFile(paths.AuditLog("eglive23"))
		if err != nil {
			return false
		}
		if strings.Contains(string(b), secretVal) {
			t.Fatalf("exposed secret leaked into audit.jsonl:\n%s", b)
		}
		return strings.Contains(string(b), "/hook/[API_SECRET]/ping")
	})

	// Verify policy.log returns the net entries.
	rawLog, err := host.HolderRequest("eglive23", "policy.log", api.PolicyLogArgs{Max: 10}, 5*time.Second)
	if err != nil {
		t.Fatalf("policy.log failed: %v", err)
	}
	var entries []egress.AuditEntry
	if err := json.Unmarshal(rawLog, &entries); err != nil || len(entries) == 0 {
		t.Fatalf("policy.log entries = %+v, %v", entries, err)
	}
	_ = sup
}

func TestEgressKilledFailsClosedAndStartsNoSecondProcess(t *testing.T) {
	ts, originCA := newEgressOriginTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), "api.anthropic.com")
	defer ts.Close()

	pol, _, err := egress.MergeLayers([]api.Layer{{Kind: api.LayerSession, Unknown: api.UnknownDeny}})
	if err != nil {
		t.Fatal(err)
	}
	sup, proxyURL := startEgressSession(t, "egkill23", "claude", pol, "", ts.Listener.Addr().String(), "", originCA)
	gotAlerts := alertsTo(t)
	hc := proxyClient(t, proxyURL)

	// Confirm egress works before killing it.
	const modelReq = `{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}]}`
	resp, err := hc.Post("https://api.anthropic.com/v1/messages", "application/json", strings.NewReader(modelReq))
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("before kill, api.anthropic.com = %v, %v", resp, err)
	}
	_ = resp.Body.Close()

	sup.mu.Lock()
	origPID, child := sup.eg.pid, sup.eg.cmd.Process
	sup.mu.Unlock()

	// Kill the child through the process that the supervisor started.
	if err := child.Kill(); err != nil {
		t.Fatalf("kill egress %d: %v", origPID, err)
	}

	// Armed becomes false and sandbox_error alert fires.
	waitFor(t, "Armed=false after egress SIGKILL", func() bool {
		m, err := host.LoadManifest("egkill23")
		return err == nil && !m.Armed
	})
	waitAlerts(t, gotAlerts, alerts.SandboxError)

	// New flows fail closed immediately, and NO second egress process starts.
	if resp2, err := hc.Post("https://api.anthropic.com/v1/messages", "application/json", strings.NewReader(modelReq)); err == nil && resp2.StatusCode == http.StatusOK {
		_ = resp2.Body.Close()
		t.Fatal("flow succeeded after egress was killed")
	}
	sup.mu.Lock()
	curPID := sup.eg.pid
	dead := sup.eg.dead.Load()
	sup.mu.Unlock()
	if curPID != origPID || !dead {
		t.Fatalf("egress state after kill: pid=%d (orig %d) dead=%v; must never restart", curPID, origPID, dead)
	}

	// Verify guest.Files manifest did not crash the session's holder.
	if _, err := os.Stat(guest.Files(paths.SandboxRoot("egkill23")).Manifest); err != nil {
		t.Errorf("guest holder manifest missing: %v", err)
	}
}
