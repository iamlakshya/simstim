//go:build linux

package supervisor

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/guest"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/version"
	"github.com/iamlakshya/simstim/internal/wall/process"
)

type mockGatewayUpstream struct {
	server *httptest.Server
	tlsCfg *tls.Config
	mu     sync.Mutex
	seen   []capturedReq
}

type capturedReq struct {
	Host   string
	Method string
	Path   string
	Header http.Header
	Body   string
}

func startMockGatewayUpstream(t *testing.T, dnsNames []string) *mockGatewayUpstream {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: dnsNames[0]},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:              dnsNames,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)

	m := &mockGatewayUpstream{
		tlsCfg: &tls.Config{
			RootCAs:    pool,
			MinVersion: tls.VersionTLS12,
		},
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		m.mu.Lock()
		m.seen = append(m.seen, capturedReq{
			Host:   r.Host,
			Method: r.Method,
			Path:   r.URL.Path,
			Header: r.Header.Clone(),
			Body:   string(body),
		})
		m.mu.Unlock()

		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/messages"):
			writeMockAnthropicResponse(w, r, body)
		case strings.HasPrefix(r.URL.Path, "/v1/responses"):
			writeMockOpenAIResponse(w, r, body)
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"ok":true,"service":"custom"}`)
		}
	}))
	srv.TLS = &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		MinVersion:   tls.VersionTLS12,
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	m.server = srv
	return m
}

func writeMockAnthropicResponse(w http.ResponseWriter, _ *http.Request, body []byte) {
	var req struct {
		Stream bool `json:"stream"`
	}
	_ = json.Unmarshal(body, &req)
	if req.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w,
			"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-opus-5-5\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n"+
				"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"+
				"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello from mock anthropic gateway\"}}\n\n"+
				"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"+
				"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":5}}\n\n"+
				"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
		)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5-5","content":[{"type":"text","text":"hello from mock anthropic gateway"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5}}`)
}

func writeMockOpenAIResponse(w http.ResponseWriter, _ *http.Request, _ []byte) {
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = io.WriteString(w,
		"event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"status\":\"in_progress\",\"model\":\"gpt-5\"}}\n\n"+
			"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"status\":\"in_progress\",\"role\":\"assistant\",\"content\":[]}}\n\n"+
			"event: response.content_part.added\ndata: {\"type\":\"response.content_part.added\",\"item_id\":\"msg_1\",\"output_index\":0,\"content_index\":0,\"part\":{\"type\":\"output_text\",\"text\":\"\"}}\n\n"+
			"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"output_index\":0,\"content_index\":0,\"delta\":\"hello from mock openai gateway\"}\n\n"+
			"event: response.output_text.done\ndata: {\"type\":\"response.output_text.done\",\"item_id\":\"msg_1\",\"output_index\":0,\"content_index\":0,\"text\":\"hello from mock openai gateway\"}\n\n"+
			"event: response.content_part.done\ndata: {\"type\":\"response.content_part.done\",\"item_id\":\"msg_1\",\"output_index\":0,\"content_index\":0,\"part\":{\"type\":\"output_text\",\"text\":\"hello from mock openai gateway\"}}\n\n"+
			"event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello from mock openai gateway\"}]}}\n\n"+
			"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"model\":\"gpt-5\",\"output\":[{\"id\":\"msg_1\",\"type\":\"message\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello from mock openai gateway\"}]}],\"usage\":{\"input_tokens\":12,\"output_tokens\":6,\"total_tokens\":18}}}\n\n",
	)
}

func (m *mockGatewayUpstream) requests() []capturedReq {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]capturedReq, len(m.seen))
	copy(out, m.seen)
	return out
}

type staticIPResolver struct {
	addr netip.Addr
}

func (r staticIPResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return []netip.Addr{r.addr}, nil
}

func startInProcessEgressForMock(t *testing.T, id, agentKind string, routes []egress.GatewayRouteConfig, mock *mockGatewayUpstream) *egress.Client {
	t.Helper()
	supConn, egConn := net.Pipe()
	mockAddr := mock.server.Listener.Addr().String()
	addrs, err := egress.NewAddressPolicyWithHosts(nil)
	if err != nil {
		t.Fatal(err)
	}
	dialer := &egress.SafeDialer{
		Addrs:    addrs,
		Resolver: staticIPResolver{addr: netip.MustParseAddr("198.51.100.10")},
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, mockAddr)
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	egDone := make(chan error, 1)
	go func() {
		egDone <- egress.RunOnConn(ctx, id, egConn, &egress.ServerOptions{
			Addrs:      addrs,
			Dialer:     dialer,
			GatewayTLS: mock.tlsCfg,
		})
	}()
	t.Cleanup(func() {
		cancel()
		_ = supConn.Close()
		_ = egConn.Close()
		<-egDone
	})

	initCtx, initCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer initCancel()
	cl, err := egress.NewClient(initCtx, supConn, egress.StartArgs{
		Session:       id,
		Agent:         agentKind,
		GatewayRoutes: routes,
	}, nil)
	if err != nil {
		t.Fatalf("egress.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = cl.Close() })
	return cl
}

func randomHexSecret(t *testing.T, prefix string) string {
	t.Helper()
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	return prefix + hex.EncodeToString(b[:])
}

func assertNoSecretOnDisk(t *testing.T, secret string, dirs ...string) {
	t.Helper()
	needle := []byte(secret)
	for _, root := range dirs {
		if root == "" {
			continue
		}
		if _, err := os.Stat(root); os.IsNotExist(err) {
			continue
		}
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return filepath.SkipDir
			}
			if d.IsDir() || !d.Type().IsRegular() {
				return nil
			}
			b, rerr := os.ReadFile(p)
			if rerr == nil && bytes.Contains(b, needle) {
				t.Errorf("secret leaked on disk in %s", p)
			}
			return nil
		})
	}
}

func buildTestHelper(t *testing.T, ctx context.Context, tmp string) string {
	t.Helper()
	if prebuilt := os.Getenv(brand.Env("LIVE_HELPER_BIN")); prebuilt != "" {
		return prebuilt
	}
	helperBin := filepath.Join(tmp, "bin", brand.HostBinary)
	if err := os.MkdirAll(filepath.Dir(helperBin), 0o700); err != nil {
		t.Fatal(err)
	}
	ldflags := "-X github.com/iamlakshya/" + brand.Name + "/internal/version.Version=" + version.Version
	buildCmd := exec.CommandContext(ctx, "go", "build", "-buildvcs=false", "-ldflags", ldflags, "-o", helperBin, "../../cmd/host")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("build cmd/host: %v\n%s", err, string(out))
	}
	return helperBin
}

func deliverSecretsToSupervisor(t *testing.T, id string, secrets map[string]api.Secret) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		_, err := host.HolderRequest(id, "secrets", host.SecretsPayload{Env: secrets}, 2*time.Second)
		if err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("failed to deliver secrets to supervisor %s", id)
}

func waitForSupervisorReady(t *testing.T, id string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if lm, err := host.LoadManifest(id); err == nil && lm.Status.State != host.StateStarting {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("supervisor %s never left starting state", id)
}

func waitForOutput(t *testing.T, id, want string) string {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		raw, err := host.HolderRequest(id, "peek", map[string]int{"bytes": 65536}, 2*time.Second)
		if err == nil {
			var res struct {
				Data []byte `json:"data"`
			}
			if json.Unmarshal(raw, &res) == nil {
				last = string(res.Data)
				if strings.Contains(last, want) {
					return last
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("output never contained %q; got:\n%s", want, last)
	return last
}

func TestSupervisorProcessWallBoundSecretsEndToEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	probe := process.Driver{}.Probe(ctx)
	if !probe.OK {
		t.Skipf("process wall unavailable: %s", probe.Reason)
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 required for guest HTTP probe")
	}

	tmp := t.TempDir()
	helperBin := buildTestHelper(t, ctx, tmp)
	mock := startMockGatewayUpstream(t, []string{"api.anthropic.com", "api.openai.com", "api.acme.test"})

	t.Run("claude_gateway_and_bound_secret", func(t *testing.T) {
		subTmp := t.TempDir()
		t.Setenv("HOME", subTmp)
		t.Setenv("XDG_RUNTIME_DIR", filepath.Join(subTmp, "run"))
		t.Setenv("XDG_STATE_HOME", filepath.Join(subTmp, "state"))
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(subTmp, "cfg"))
		t.Setenv("XDG_DATA_HOME", filepath.Join(subTmp, "data"))

		wsHost := filepath.Join(subTmp, "repo")
		if err := os.MkdirAll(wsHost, 0o700); err != nil {
			t.Fatal(err)
		}
		realKey := randomHexSecret(t, "sk-ant-api03-TESTREAL-")
		id := "bscl01ab"
		now := time.Now().UTC()

		pyScript := `import os, socket, urllib.request, json, glob
print("BASE=" + os.environ.get("ANTHROPIC_BASE_URL", ""))
print("KEY=" + os.environ.get("ANTHROPIC_API_KEY", ""))
# Direct CONNECT to api.anthropic.com must be 403.
s = socket.create_connection(("127.0.0.1", 3128), timeout=5)
s.sendall(b"CONNECT api.anthropic.com:443 HTTP/1.1\r\nHost: api.anthropic.com:443\r\n\r\n")
first = s.recv(512).decode("utf-8", "replace").splitlines()[0]
s.close()
print("DIRECT=" + first)
# Call gateway with the placeholder key; egress injects the real key.
req = urllib.request.Request(
    os.environ["ANTHROPIC_BASE_URL"] + "/v1/messages",
    data=json.dumps({"model": "claude-opus-5-5", "messages": [{"role": "user", "content": "hi"}]}).encode(),
    headers={"Content-Type": "application/json", "x-api-key": os.environ["ANTHROPIC_API_KEY"]},
)
with urllib.request.urlopen(req, timeout=10) as resp:
    print("GW_STATUS=" + str(resp.status))
    print("GW_BODY=" + resp.read().decode())
# Snapshot sandbox env, /proc/*/environ, /proc/*/cmdline, /home/agent and /run for trusted-side scan.
with open("/work/repo/sandbox-dump.bin", "wb") as out:
    for k, v in sorted(os.environ.items()):
        out.write((k + "=" + v + "\n").encode())
    for p in sorted(glob.glob("/proc/*/environ") + glob.glob("/proc/*/cmdline")):
        try:
            out.write(b"\n--- " + p.encode() + b" ---\n")
            out.write(open(p, "rb").read())
        except Exception:
            pass
    for root in ("/home/agent", "/run"):
        for dp, _, fns in os.walk(root):
            for fn in sorted(fns):
                fp = os.path.join(dp, fn)
                try:
                    if os.path.isfile(fp):
                        out.write(b"\n--- " + fp.encode() + b" ---\n")
                        out.write(open(fp, "rb").read())
                except Exception:
                    pass
print("DUMP_OK=1")
print("CLAUDE_DONE")
`
		m := &host.Manifest{
			Schema:    1,
			ID:        id,
			Name:      "claude-bound",
			CreatedAt: now,
			Agent:     host.AgentInfo{Kind: "claude", Argv: []string{"python3", "-c", pyScript}},
			Cwd:       "/work/repo",
			Env: []string{
				"HOME=" + process.GuestHome,
				"USER=agent",
				"LOGNAME=agent",
				"LANG=C.UTF-8",
				"PATH=/usr/local/bin:/usr/bin:/bin",
				"TERM=xterm-256color",
				"HTTP_PROXY=" + process.GuestProxyURL,
				"HTTPS_PROXY=" + process.GuestProxyURL,
				"NO_PROXY=localhost,127.0.0.1,::1",
			},
			Term:    host.TermInfo{Term: "xterm-256color", Cols: 120, Rows: 30},
			Run:     host.Runtime{Socket: paths.Socket(id), BootID: host.BootID()},
			Status:  host.Status{State: host.StateStarting, Since: now},
			Secrets: []string{"ANTHROPIC_API_KEY"},
			Wall: &host.WallRecord{
				Level:          "process",
				Driver:         process.DriverName,
				Version:        probe.Version,
				Preset:         string(egress.PresetBalanced),
				WorkspaceHost:  wsHost,
				WorkspaceGuest: "/work/repo",
				StateDirs:      []string{".claude/projects"},
				BoundSecrets: []api.SecretBind{{
					Name:   "ANTHROPIC_API_KEY",
					Hosts:  []string{"api.anthropic.com"},
					Header: "x-api-key",
					Format: "{value}",
					Route:  "anthropic",
				}},
				At: now,
			},
		}
		if err := m.Save(); err != nil {
			t.Fatal(err)
		}

		egClient := startInProcessEgressForMock(t, id, "claude", egress.DefaultGatewayRoutes("claude"), mock)
		sb := NewProcessSandbox(id, helperBin, m)
		supDone := make(chan error, 1)
		go func() {
			supDone <- RunWith(id, sb, Options{
				HeartbeatTimeout: 10 * time.Second,
				TickInterval:     200 * time.Millisecond,
				StopGrace:        3 * time.Second,
				Egress:           egClient,
			})
		}()

		deliverSecretsToSupervisor(t, id, map[string]api.Secret{"ANTHROPIC_API_KEY": api.Secret(realKey)})
		waitForSupervisorReady(t, id)
		out := waitForOutput(t, id, "CLAUDE_DONE")

		wantPlaceholder := guest.GatewayPlaceholder("ANTHROPIC_API_KEY")
		for _, expect := range []string{
			"BASE=http://127.0.0.1:3128/gw/anthropic",
			"KEY=" + wantPlaceholder,
			"DIRECT=HTTP/1.1 403 Forbidden",
			"GW_STATUS=200",
			"hello from mock anthropic gateway",
			"DUMP_OK=1",
		} {
			if !strings.Contains(out, expect) {
				t.Errorf("claude output missing %q:\n%s", expect, out)
			}
		}
		if strings.Contains(out, realKey) {
			t.Fatalf("real key leaked into sandbox terminal output!")
		}
		dumpBytes, err := os.ReadFile(filepath.Join(wsHost, "sandbox-dump.bin"))
		if err != nil {
			t.Fatalf("read sandbox dump: %v", err)
		}
		if bytes.Contains(dumpBytes, []byte(realKey)) {
			t.Fatalf("real key leaked into sandbox env, /proc, /home/agent or /run!")
		}
		if !bytes.Contains(dumpBytes, []byte(wantPlaceholder)) {
			t.Fatalf("sandbox dump missing placeholder %q", wantPlaceholder)
		}

		var gotReal bool
		for _, req := range mock.requests() {
			if req.Path == "/v1/messages" && req.Header.Get("X-Api-Key") == realKey {
				gotReal = true
			}
		}
		if !gotReal {
			t.Fatalf("mock upstream never received real x-api-key; saw %+v", mock.requests())
		}
		assertNoSecretOnDisk(t, realKey, paths.SessionDir(id), paths.RuntimeDir(), filepath.Join(subTmp, "state"), filepath.Join(subTmp, "data"))

		_, _ = host.HolderRequest(id, "kill", nil, 3*time.Second)
		<-supDone
	})

	t.Run("codex_gateway_config_and_bound_secret", func(t *testing.T) {
		subTmp := t.TempDir()
		t.Setenv("HOME", subTmp)
		t.Setenv("XDG_RUNTIME_DIR", filepath.Join(subTmp, "run"))
		t.Setenv("XDG_STATE_HOME", filepath.Join(subTmp, "state"))
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(subTmp, "cfg"))
		t.Setenv("XDG_DATA_HOME", filepath.Join(subTmp, "data"))

		// Sentinel in host user's own ~/.codex/config.toml must NEVER be modified.
		hostCodexCfg := filepath.Join(subTmp, ".codex", "config.toml")
		if err := os.MkdirAll(filepath.Dir(hostCodexCfg), 0o700); err != nil {
			t.Fatal(err)
		}
		const hostSentinel = "# host-user-codex-config-untouched\n"
		if err := os.WriteFile(hostCodexCfg, []byte(hostSentinel), 0o600); err != nil {
			t.Fatal(err)
		}

		wsHost := filepath.Join(subTmp, "repo")
		if err := os.MkdirAll(wsHost, 0o700); err != nil {
			t.Fatal(err)
		}
		realKey := randomHexSecret(t, "sk-proj-TESTCODEX-")
		id := "bscx02cd"
		now := time.Now().UTC()

		pyScript := `import os, urllib.request, json
print("BASE=" + os.environ.get("OPENAI_BASE_URL", ""))
print("KEY=" + os.environ.get("OPENAI_API_KEY", ""))
cfg = open("/home/agent/.codex/config.toml").read()
print("CFG_HAS_PROVIDER=" + str('model_provider = "gateway"' in cfg))
print("CFG_HAS_URL=" + str('base_url = "http://127.0.0.1:3128/gw/openai/v1"' in cfg))
print("CFG_HAS_ENVKEY=" + str('env_key = "OPENAI_API_KEY"' in cfg))
print("CFG_HAS_WIRE=" + str('wire_api = "responses"' in cfg))
print("CFG_NO_WS=" + str('supports_websockets = false' in cfg))
req = urllib.request.Request(
    os.environ["OPENAI_BASE_URL"] + "/responses",
    data=json.dumps({"model": "gpt-5", "input": "hi"}).encode(),
    headers={"Content-Type": "application/json", "Authorization": "Bearer " + os.environ["OPENAI_API_KEY"]},
)
with urllib.request.urlopen(req, timeout=10) as resp:
    print("GW_STATUS=" + str(resp.status))
    print("GW_OK=" + str("hello from mock openai gateway" in resp.read().decode()))
print("CODEX_DONE")
`
		m := &host.Manifest{
			Schema:    1,
			ID:        id,
			Name:      "codex-bound",
			CreatedAt: now,
			Agent:     host.AgentInfo{Kind: "codex", Argv: []string{"python3", "-c", pyScript}},
			Cwd:       "/work/repo",
			Env: []string{
				"HOME=" + process.GuestHome,
				"USER=agent",
				"LOGNAME=agent",
				"LANG=C.UTF-8",
				"PATH=/usr/local/bin:/usr/bin:/bin",
				"TERM=xterm-256color",
				"HTTP_PROXY=" + process.GuestProxyURL,
				"HTTPS_PROXY=" + process.GuestProxyURL,
				"NO_PROXY=localhost,127.0.0.1,::1",
			},
			Term:    host.TermInfo{Term: "xterm-256color", Cols: 120, Rows: 30},
			Run:     host.Runtime{Socket: paths.Socket(id), BootID: host.BootID()},
			Status:  host.Status{State: host.StateStarting, Since: now},
			Secrets: []string{"OPENAI_API_KEY"},
			Wall: &host.WallRecord{
				Level:          "process",
				Driver:         process.DriverName,
				Version:        probe.Version,
				Preset:         string(egress.PresetBalanced),
				WorkspaceHost:  wsHost,
				WorkspaceGuest: "/work/repo",
				StateDirs:      []string{".codex/sessions"},
				BoundSecrets: []api.SecretBind{{
					Name:   "OPENAI_API_KEY",
					Hosts:  []string{"api.openai.com"},
					Header: "Authorization",
					Format: "Bearer {value}",
					Route:  "openai",
				}},
				At: now,
			},
		}
		if err := m.Save(); err != nil {
			t.Fatal(err)
		}

		egClient := startInProcessEgressForMock(t, id, "codex", egress.DefaultGatewayRoutes("codex"), mock)
		sb := NewProcessSandbox(id, helperBin, m)
		supDone := make(chan error, 1)
		go func() {
			supDone <- RunWith(id, sb, Options{
				HeartbeatTimeout: 10 * time.Second,
				TickInterval:     200 * time.Millisecond,
				StopGrace:        3 * time.Second,
				Egress:           egClient,
			})
		}()

		deliverSecretsToSupervisor(t, id, map[string]api.Secret{"OPENAI_API_KEY": api.Secret(realKey)})
		waitForSupervisorReady(t, id)
		out := waitForOutput(t, id, "CODEX_DONE")

		wantPlaceholder := guest.GatewayPlaceholder("OPENAI_API_KEY")
		for _, expect := range []string{
			"BASE=http://127.0.0.1:3128/gw/openai/v1",
			"KEY=" + wantPlaceholder,
			"CFG_HAS_PROVIDER=True",
			"CFG_HAS_URL=True",
			"CFG_HAS_ENVKEY=True",
			"CFG_HAS_WIRE=True",
			"CFG_NO_WS=True",
			"GW_STATUS=200",
			"GW_OK=True",
		} {
			if !strings.Contains(out, expect) {
				t.Errorf("codex output missing %q:\n%s", expect, out)
			}
		}
		if strings.Contains(out, realKey) {
			t.Fatalf("real key leaked into sandbox output!")
		}
		gotHostCfg, err := os.ReadFile(hostCodexCfg)
		if err != nil || string(gotHostCfg) != hostSentinel {
			t.Fatalf("host ~/.codex/config.toml was modified: %q (%v)", string(gotHostCfg), err)
		}

		var gotReal bool
		for _, req := range mock.requests() {
			if req.Path == "/v1/responses" && req.Header.Get("Authorization") == "Bearer "+realKey {
				gotReal = true
			}
		}
		if !gotReal {
			t.Fatalf("mock upstream never received real Authorization header")
		}
		assertNoSecretOnDisk(t, realKey, paths.SessionDir(id), paths.RuntimeDir(), filepath.Join(subTmp, "state"), filepath.Join(subTmp, "data"))

		_, _ = host.HolderRequest(id, "kill", nil, 3*time.Second)
		<-supDone
	})

	t.Run("custom_bound_secret_exposed_secret_and_live_arm", func(t *testing.T) {
		subTmp := t.TempDir()
		t.Setenv("HOME", subTmp)
		t.Setenv("XDG_RUNTIME_DIR", filepath.Join(subTmp, "run"))
		t.Setenv("XDG_STATE_HOME", filepath.Join(subTmp, "state"))
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(subTmp, "cfg"))
		t.Setenv("XDG_DATA_HOME", filepath.Join(subTmp, "data"))

		wsHost := filepath.Join(subTmp, "repo")
		if err := os.MkdirAll(wsHost, 0o700); err != nil {
			t.Fatal(err)
		}
		acmeKey1 := randomHexSecret(t, "acme-real-1-")
		acmeKey2 := randomHexSecret(t, "acme-real-2-")
		exposedVal := randomHexSecret(t, "exposed-only-")
		id := "bscu03ef"
		now := time.Now().UTC()

		m := &host.Manifest{
			Schema:    1,
			ID:        id,
			Name:      "custom-bound",
			CreatedAt: now,
			Agent:     host.AgentInfo{Kind: "shell", Argv: []string{"/bin/sh"}},
			Cwd:       "/work/repo",
			Env: []string{
				"HOME=" + process.GuestHome,
				"USER=agent",
				"LOGNAME=agent",
				"LANG=C.UTF-8",
				"PATH=/usr/local/bin:/usr/bin:/bin",
				"TERM=xterm-256color",
				"HTTP_PROXY=" + process.GuestProxyURL,
				"HTTPS_PROXY=" + process.GuestProxyURL,
				"NO_PROXY=localhost,127.0.0.1,::1",
			},
			Term:    host.TermInfo{Term: "xterm-256color", Cols: 120, Rows: 30},
			Run:     host.Runtime{Socket: paths.Socket(id), BootID: host.BootID()},
			Status:  host.Status{State: host.StateStarting, Since: now},
			Secrets: []string{"ACME_KEY", "EXPOSED_VAR"},
			Wall: &host.WallRecord{
				Level:          "process",
				Driver:         process.DriverName,
				Version:        probe.Version,
				Preset:         string(egress.PresetBalanced),
				WorkspaceHost:  wsHost,
				WorkspaceGuest: "/work/repo",
				BoundSecrets: []api.SecretBind{{
					Name:   "ACME_KEY",
					Hosts:  []string{"api.acme.test"},
					Header: "X-Acme-Token",
					Format: "{value}",
					Route:  "acme",
				}},
				ExposedSecrets: []string{"EXPOSED_VAR"},
				At:             now,
			},
		}
		if err := m.Save(); err != nil {
			t.Fatal(err)
		}

		routes := append(egress.DefaultGatewayRoutes("shell"), egress.GatewayRouteConfig{
			Route:    "acme",
			Upstream: "https://api.acme.test",
		})
		egClient := startInProcessEgressForMock(t, id, "shell", routes, mock)
		sb := NewProcessSandbox(id, helperBin, m)
		supDone := make(chan error, 1)
		go func() {
			supDone <- RunWith(id, sb, Options{
				HeartbeatTimeout: 10 * time.Second,
				TickInterval:     200 * time.Millisecond,
				StopGrace:        3 * time.Second,
				Egress:           egClient,
			})
		}()

		deliverSecretsToSupervisor(t, id, map[string]api.Secret{
			"ACME_KEY":    api.Secret(acmeKey1),
			"EXPOSED_VAR": api.Secret(exposedVal),
		})
		waitForSupervisorReady(t, id)

		cmd1 := `python3 -c 'import os, urllib.request; print("ACME_ENV=" + os.environ.get("ACME_KEY","")); print("EXP_ENV=" + os.environ.get("EXPOSED_VAR","")); r=urllib.request.urlopen("http://127.0.0.1:3128/gw/acme/v1/ping", timeout=10); print("ACME_STATUS1=" + str(r.status))'`
		if _, err := host.HolderRequest(id, "send", map[string]any{"text": cmd1, "enter": true}, 3*time.Second); err != nil {
			t.Fatal(err)
		}
		out := waitForOutput(t, id, "ACME_STATUS1=200")
		if !strings.Contains(out, "ACME_ENV="+guest.GatewayPlaceholder("ACME_KEY")) {
			t.Errorf("ACME_KEY should be placeholder in guest env:\n%s", out)
		}
		if !strings.Contains(out, "EXP_ENV="+exposedVal) {
			t.Errorf("EXPOSED_VAR missing from guest env:\n%s", out)
		}
		if strings.Contains(out, acmeKey1) {
			t.Fatal("bound secret ACME_KEY leaked into guest env!")
		}

		// Live re-arm with acmeKey2 via supervisor "arm" op.
		if _, err := host.HolderRequest(id, "arm", host.SecretsPayload{
			Env: map[string]api.Secret{"ACME_KEY": api.Secret(acmeKey2)},
		}, 3*time.Second); err != nil {
			t.Fatalf("live arm failed: %v", err)
		}
		cmd2 := `python3 -c 'import urllib.request; r=urllib.request.urlopen("http://127.0.0.1:3128/gw/acme/v1/ping2", timeout=10); print("ACME_STATUS2=" + str(r.status))'`
		if _, err := host.HolderRequest(id, "send", map[string]any{"text": cmd2, "enter": true}, 3*time.Second); err != nil {
			t.Fatal(err)
		}
		_ = waitForOutput(t, id, "ACME_STATUS2=200")

		var got1, got2 bool
		for _, req := range mock.requests() {
			if req.Path == "/v1/ping" && req.Header.Get("X-Acme-Token") == acmeKey1 {
				got1 = true
			}
			if req.Path == "/v1/ping2" && req.Header.Get("X-Acme-Token") == acmeKey2 {
				got2 = true
			}
		}
		if !got1 || !got2 {
			t.Fatalf("expected mock upstream to see both initial and re-armed ACME tokens: got1=%v got2=%v", got1, got2)
		}
		assertNoSecretOnDisk(t, acmeKey1, paths.SessionDir(id), paths.RuntimeDir())
		assertNoSecretOnDisk(t, acmeKey2, paths.SessionDir(id), paths.RuntimeDir())
		assertNoSecretOnDisk(t, exposedVal, paths.SessionDir(id), paths.RuntimeDir())

		_, _ = host.HolderRequest(id, "kill", nil, 3*time.Second)
		<-supDone
	})
}

// TestLiveHostABoundSecretsRealAgents runs real Claude Code and real Codex
// through the real supervisor + bubblewrap process wall + wall-init + egress
// gateway on a test host against a local verified-TLS mock upstream. Opt-in via
// brand.Env("LIVE_HOST_TEST")=1.
func TestLiveHostABoundSecretsRealAgents(t *testing.T) {
	if os.Getenv(brand.Env("LIVE_HOST_TEST")) != "1" {
		t.Skip("set " + brand.Env("LIVE_HOST_TEST") + "=1 to run live real-agent proof")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	probe := process.Driver{}.Probe(ctx)
	if !probe.OK {
		t.Fatalf("process wall unavailable: %s (%s)", probe.Reason, probe.Fix)
	}
	binDir := os.Getenv(brand.Env("LIVE_AGENT_BIN_DIR"))
	if binDir == "" {
		realHome, err := os.UserHomeDir()
		if err != nil {
			t.Fatal(err)
		}
		binDir = filepath.Join(realHome, ".local", "bin")
	}
	claudeBin := filepath.Join(binDir, "claude")
	codexBin := filepath.Join(binDir, "codex")
	for _, p := range []string{claudeBin, codexBin} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("required real agent binary missing at %s: %v", p, err)
		}
	}

	tmp := t.TempDir()
	helperBin := buildTestHelper(t, ctx, tmp)
	mock := startMockGatewayUpstream(t, []string{"api.anthropic.com", "api.openai.com"})

	runRealAgent := func(t *testing.T, id, agentKind, realBin, secretName, secretPrefix, route, header, format string, agentArgs []string, wantOut string) {
		t.Helper()
		subTmp := t.TempDir()
		t.Setenv("HOME", subTmp)
		t.Setenv("XDG_RUNTIME_DIR", filepath.Join(subTmp, "run"))
		t.Setenv("XDG_STATE_HOME", filepath.Join(subTmp, "state"))
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(subTmp, "cfg"))
		t.Setenv("XDG_DATA_HOME", filepath.Join(subTmp, "data"))

		wsHost := filepath.Join(subTmp, "repo")
		if err := os.MkdirAll(wsHost, 0o700); err != nil {
			t.Fatal(err)
		}
		resolved, err := filepath.EvalSymlinks(realBin)
		if err != nil {
			t.Fatal(err)
		}
		guestBin := "/opt/agents/bin/" + filepath.Base(realBin)
		realKey := randomHexSecret(t, secretPrefix)

		agentRow, _ := agents.Find(agentKind)
		shellCmd := fmt.Sprintf(
			`%s %s; rc=$?; echo "AGENT_RC=$rc"; env | grep -E '^(ANTHROPIC_|OPENAI_|CODEX_)' || true; `+
				`if [ -f /home/agent/.codex/config.toml ]; then echo "===CODEX_CFG==="; cat /home/agent/.codex/config.toml; fi; `+
				`python3 -c 'import os, glob
with open("/work/repo/sandbox-dump.bin", "wb") as out:
    for k, v in sorted(os.environ.items()):
        out.write((k + "=" + v + "\n").encode())
    for p in sorted(glob.glob("/proc/*/environ") + glob.glob("/proc/*/cmdline")):
        try:
            out.write(b"\n--- " + p.encode() + b" ---\n" + open(p, "rb").read())
        except Exception:
            pass
    for r in ("/home/agent", "/run"):
        for dp, _, fns in os.walk(r):
            for fn in sorted(fns):
                fp = os.path.join(dp, fn)
                try:
                    if os.path.isfile(fp):
                        out.write(b"\n--- " + fp.encode() + b" ---\n" + open(fp, "rb").read())
                except Exception:
                    pass
print("DUMP_OK=1")'; echo "LIVE_AGENT_DONE"; sleep 30`,
			guestBin, strings.Join(agentArgs, " "),
		)
		now := time.Now().UTC()
		m := &host.Manifest{
			Schema:    1,
			ID:        id,
			Name:      "live-" + agentKind,
			CreatedAt: now,
			Agent:     host.AgentInfo{Kind: agentKind, Argv: []string{"/bin/sh", "-c", shellCmd}},
			Cwd:       "/work/repo",
			Env: []string{
				"HOME=" + process.GuestHome,
				"USER=agent",
				"LOGNAME=agent",
				"LANG=C.UTF-8",
				"PATH=/opt/agents/bin:/usr/local/bin:/usr/bin:/bin",
				"TERM=xterm-256color",
				"HTTP_PROXY=" + process.GuestProxyURL,
				"HTTPS_PROXY=" + process.GuestProxyURL,
				"NO_PROXY=localhost,127.0.0.1,::1",
				"NODE_USE_ENV_PROXY=1",
			},
			Term:    host.TermInfo{Term: "xterm-256color", Cols: 120, Rows: 30},
			Run:     host.Runtime{Socket: paths.Socket(id), BootID: host.BootID()},
			Status:  host.Status{State: host.StateStarting, Since: now},
			Secrets: []string{secretName},
			Wall: &host.WallRecord{
				Level:          "process",
				Driver:         process.DriverName,
				Version:        probe.Version,
				Preset:         string(egress.PresetBalanced),
				WorkspaceHost:  wsHost,
				WorkspaceGuest: "/work/repo",
				StateDirs:      agentRow.StateDirs,
				ExtraRO:        []host.WallMount{{Source: resolved, Dest: guestBin, ReadOnly: true}},
				BoundSecrets: []api.SecretBind{{
					Name:   secretName,
					Hosts:  agentRow.Secrets[0].Hosts,
					Header: header,
					Format: format,
					Route:  route,
				}},
				At: now,
			},
		}
		if err := m.Save(); err != nil {
			t.Fatal(err)
		}

		egClient := startInProcessEgressForMock(t, id, agentKind, egress.DefaultGatewayRoutes(agentKind), mock)
		sb := NewProcessSandbox(id, helperBin, m)
		supDone := make(chan error, 1)
		go func() {
			supDone <- RunWith(id, sb, Options{
				HeartbeatTimeout: 15 * time.Second,
				TickInterval:     200 * time.Millisecond,
				StopGrace:        3 * time.Second,
				Egress:           egClient,
			})
		}()

		deliverSecretsToSupervisor(t, id, map[string]api.Secret{secretName: api.Secret(realKey)})
		waitForSupervisorReady(t, id)
		out := waitForOutput(t, id, "LIVE_AGENT_DONE")
		t.Logf("%s live output:\n%s", agentKind, out)

		if !strings.Contains(out, "AGENT_RC=0") {
			t.Fatalf("%s exited non-zero:\n%s", agentKind, out)
		}
		if !strings.Contains(out, wantOut) {
			t.Fatalf("%s missing expected reply %q:\n%s", agentKind, wantOut, out)
		}
		if !strings.Contains(out, secretName+"="+guest.GatewayPlaceholder(secretName)) {
			t.Fatalf("%s missing placeholder env var:\n%s", agentKind, out)
		}
		if !strings.Contains(out, "DUMP_OK=1") {
			t.Fatalf("%s sandbox dump failed:\n%s", agentKind, out)
		}
		if strings.Contains(out, realKey) {
			t.Fatalf("%s leaked real secret into output", agentKind)
		}
		dumpBytes, err := os.ReadFile(filepath.Join(wsHost, "sandbox-dump.bin"))
		if err != nil {
			t.Fatalf("%s read sandbox dump: %v", agentKind, err)
		}
		if bytes.Contains(dumpBytes, []byte(realKey)) {
			t.Fatalf("%s real key leaked into sandbox env, /proc, /home/agent or /run!", agentKind)
		}
		if !bytes.Contains(dumpBytes, []byte(guest.GatewayPlaceholder(secretName))) {
			t.Fatalf("%s sandbox dump missing placeholder", agentKind)
		}

		wantHeaderVal := strings.ReplaceAll(format, "{value}", realKey)
		var matched bool
		for _, req := range mock.requests() {
			if req.Header.Get(header) == wantHeaderVal {
				matched = true
			}
		}
		if !matched {
			t.Fatalf("%s mock upstream never saw real %s header", agentKind, header)
		}
		assertNoSecretOnDisk(t, realKey, paths.SessionDir(id), paths.RuntimeDir())

		_, _ = host.HolderRequest(id, "kill", nil, 3*time.Second)
		<-supDone
	}

	t.Run("real_claude", func(t *testing.T) {
		runRealAgent(
			t, "lvcl26a1", "claude", claudeBin,
			"ANTHROPIC_API_KEY", "sk-ant-api03-LIVECLAUDE-", "anthropic", "X-Api-Key", "{value}",
			[]string{"--bare", "-p", "'Say hello'", "--output-format", "text", "--no-session-persistence"},
			"hello from mock anthropic gateway",
		)
	})

	t.Run("real_codex", func(t *testing.T) {
		runRealAgent(
			t, "lvcx26b2", "codex", codexBin,
			"OPENAI_API_KEY", "sk-proj-LIVECODEX-", "openai", "Authorization", "Bearer {value}",
			[]string{"exec", "--strict-config", "--skip-git-repo-check", "-m", "gpt-5", "'Say hello'"},
			"hello from mock openai gateway",
		)
	})
}
