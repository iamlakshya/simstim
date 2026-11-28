//go:build linux

package supervisor_test

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
	"fmt"
	"io"
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
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/alerts"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/guest"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/supervisor"
	"github.com/iamlakshya/simstim/internal/version"
)

type staticResolver struct {
	ips map[string][]netip.Addr
}

func (r staticResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	if addrs, ok := r.ips[host]; ok {
		return addrs, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

func startMockVerifiedUpstream(t *testing.T, handler http.HandlerFunc, dnsNames ...string) (*httptest.Server, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(7),
		Subject:      pkix.Name{CommonName: dnsNames[0]},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     dnsNames,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	pool := x509.NewCertPool()
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool.AddCert(parsed)

	srv := httptest.NewUnstartedServer(handler)
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv, pool
}

func TestSupervisorUsagePersistenceBudgetAlertAndReload(t *testing.T) {
	upSrv, pool := startMockVerifiedUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/messages":
			_, _ = io.WriteString(w, `{"id":"msg_1","model":"claude-sonnet-4-6","usage":{"input_tokens":80,"output_tokens":40}}`)
		case "/v1/responses":
			_, _ = io.WriteString(w, `{"id":"resp_1","model":"gpt-5","usage":{"input_tokens":60,"output_tokens":30,"total_tokens":90}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}, "api.anthropic.com", "api.openai.com")

	alertsDir := t.TempDir()
	alertsOut := filepath.Join(alertsDir, "events.jsonl")
	alertsCfgPath := filepath.Join(alertsDir, "alerts.toml")
	alertsTOML := fmt.Sprintf("events = [\"model_budget\"]\n[[destination]]\ntype = \"command\"\nrun = \"cat >> %s\"\n", alertsOut)
	if err := os.WriteFile(alertsCfgPath, []byte(alertsTOML), 0o600); err != nil {
		t.Fatal(err)
	}

	id := host.NewID()
	now := time.Now().UTC()
	m := &host.Manifest{
		Schema: 1, ID: id, Name: "budget-sess", CreatedAt: now, HostVersion: version.Version,
		Agent:  host.AgentInfo{Kind: "claude", Argv: []string{"/bin/sh"}},
		Cwd:    t.TempDir(),
		Env:    []string{"USER=agent"},
		Term:   host.TermInfo{Term: "xterm-256color", Cols: 80, Rows: 24},
		Run:    host.Runtime{Socket: paths.Socket(id), BootID: host.BootID()},
		Status: host.Status{State: host.StateStarting, Since: now},
		Wall: &host.WallRecord{
			Level:       "process",
			Driver:      "plain",
			Version:     version.Version,
			At:          now,
			PolicyTOML:  "[sandbox]\npreset = \"balanced\"\n[network]\nmodel_budget = 100\n",
			ModelBudget: 100,
		},
	}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}

	sb := newFakeSandbox(t)
	guestLink := link.New(sb.guestConn, link.Guest, link.Options{})
	t.Cleanup(func() { _ = guestLink.Close() })

	// Minimal fake guest answering the ctl "start" handshake.
	go func() {
		ctl, err := guestLink.Accept(context.Background())
		if err != nil {
			return
		}
		defer ctl.Close()
		r := bufio.NewReader(ctl)
		w := proto.NewWriter(ctl)
		f, err := proto.Read(r)
		if err != nil {
			return
		}
		var req proto.Request
		_ = json.Unmarshal(f.Payload, &req)
		res, _ := json.Marshal(guest.StartResp{AgentPID: 42, Cols: 80, Rows: 24})
		_ = w.WriteJSON(proto.TResponse, proto.Response{ID: req.ID, OK: true, Result: res})
		for {
			if _, err := proto.Read(r); err != nil {
				return
			}
		}
	}()

	ap, err := egress.NewAddressPolicyWithHosts(nil)
	if err != nil {
		t.Fatal(err)
	}
	fakePub := netip.AddrFrom4([4]byte{93, 184, 216, 34})
	res := staticResolver{ips: map[string][]netip.Addr{
		"api.anthropic.com": {fakePub},
		"api.openai.com":    {fakePub},
	}}
	dial := func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, upSrv.Listener.Addr().String())
	}

	supDone := make(chan error, 1)
	go func() {
		supDone <- supervisor.RunWith(id, sb, supervisor.Options{
			TickInterval:     50 * time.Millisecond,
			HeartbeatTimeout: 10 * time.Second,
			AlertsPath:       alertsCfgPath,
			EgressStarter: func(ctx context.Context, eid string, args egress.StartArgs, sink egress.AuditSink) (*egress.Client, func(), error) {
				args.GatewayRoutes = egress.DefaultGatewayRoutes("")
				args.BoundSecrets = []egress.BoundCredential{
					{Route: "anthropic", Name: "ANTHROPIC_API_KEY", Header: "x-api-key", Value: api.Secret("real-ant")},
					{Route: "openai", Name: "OPENAI_API_KEY", Header: "Authorization", Format: "Bearer {value}", Value: api.Secret("real-oai")},
				}
				supC, egC := net.Pipe()
				egCtx, egCancel := context.WithCancel(context.WithoutCancel(ctx))
				egDone := make(chan error, 1)
				go func() {
					sd := &egress.SafeDialer{Addrs: ap, Resolver: res, Dial: dial}
					egDone <- egress.RunOnConn(egCtx, eid, egC, &egress.ServerOptions{
						Addrs:      ap,
						Dialer:     sd,
						GatewayTLS: &tls.Config{RootCAs: pool},
					})
				}()
				cl, err := egress.NewClient(ctx, supC, args, sink)
				if err != nil {
					egCancel()
					_ = supC.Close()
					_ = egC.Close()
					<-egDone
					return nil, nil, err
				}
				return cl, func() {
					egCancel()
					_ = cl.Close()
					<-egDone
				}, nil
			},
		})
	}()
	t.Cleanup(func() {
		_ = sb.Kill()
		<-supDone
	})

	// Wait until supervisor socket answers "info".
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := host.HolderRequest(id, "info", nil, time.Second); err == nil {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	// Send gateway requests from the guest side over link.KindEgress.
	hc := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return guestLink.Open(ctx, link.KindEgress, egress.StreamHeader{Via: "explicit"})
			},
		},
		Timeout: 5 * time.Second,
	}

	// 1. First call uses 120 tokens (crosses model_budget=100, finishes normally).
	resp, err := hc.Post("http://127.0.0.1:3128/gw/anthropic/v1/messages", "application/json", strings.NewReader(`{"model":"claude-sonnet-4-6","messages":[]}`))
	if err != nil {
		t.Fatalf("first gateway call: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first gateway call status = %d, want 200", resp.StatusCode)
	}

	// Wait for manifest.json to persist usage=120.
	deadline = time.Now().Add(3 * time.Second)
	var loaded *host.Manifest
	for time.Now().Before(deadline) {
		loaded, err = host.LoadManifest(id)
		if err == nil && loaded.Usage != nil && loaded.Usage.Total.TotalTokens == 120 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if loaded == nil || loaded.Usage == nil || loaded.Usage.Total.TotalTokens != 120 || loaded.Usage.Budget != 100 {
		t.Fatalf("persisted Manifest.Usage = %+v, want total=120 budget=100", loaded.Usage)
	}

	// Query supervisor "usage" RPC directly.
	rawUsage, err := host.HolderRequest(id, "usage", nil, 2*time.Second)
	if err != nil {
		t.Fatalf("supervisor usage RPC: %v", err)
	}
	var rpcUsage api.SessionUsage
	if err := json.Unmarshal(rawUsage, &rpcUsage); err != nil || rpcUsage.Total.TotalTokens != 120 || rpcUsage.Budget != 100 {
		t.Fatalf("supervisor usage RPC = %s (%v), want total=120 budget=100", rawUsage, err)
	}

	// 2. Next two calls exceed budget -> both get 429, model_budget alert fires ONCE.
	for i := range 2 {
		r2, err := hc.Post("http://127.0.0.1:3128/gw/anthropic/v1/messages", "application/json", strings.NewReader(`{"model":"claude-sonnet-4-6","messages":[]}`))
		if err != nil {
			t.Fatalf("blocked call %d: %v", i, err)
		}
		_, _ = io.ReadAll(r2.Body)
		_ = r2.Body.Close()
		if r2.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("blocked call %d status = %d, want 429", i, r2.StatusCode)
		}
	}

	// Wait for the one model_budget alert to be delivered by the command sink.
	readAlertLines := func() []alerts.Event {
		b, err := os.ReadFile(alertsOut)
		if err != nil {
			return nil
		}
		var out []alerts.Event
		for line := range strings.SplitSeq(strings.TrimSpace(string(b)), "\n") {
			if line == "" {
				continue
			}
			var ev alerts.Event
			if json.Unmarshal([]byte(line), &ev) == nil {
				out = append(out, ev)
			}
		}
		return out
	}
	deadline = time.Now().Add(5 * time.Second)
	for len(readAlertLines()) == 0 && time.Now().Before(deadline) {
		time.Sleep(30 * time.Millisecond)
	}
	// Brief settle wait to ensure the second refused call did not fire a duplicate alert.
	time.Sleep(150 * time.Millisecond)
	gotAlerts := readAlertLines()
	if len(gotAlerts) != 1 || gotAlerts[0].Event != alerts.ModelBudget || !strings.Contains(gotAlerts[0].Summary, "120 of 100 tokens") {
		t.Fatalf("model_budget alerts = %+v, want exactly 1 alert mentioning 120 of 100 tokens", gotAlerts)
	}

	// 3. Reload policy via supervisor socket raising model_budget to 200 -> next call passes!
	reloadArgs := egress.ReloadArgs{PolicyTOML: "[sandbox]\npreset = \"balanced\"\n[network]\nmodel_budget = 200\n"}
	if _, err := host.HolderRequest(id, "policy.reload", reloadArgs, 2*time.Second); err != nil {
		t.Fatalf("supervisor policy.reload: %v", err)
	}
	r3, err := hc.Post("http://127.0.0.1:3128/gw/openai/v1/responses", "application/json", strings.NewReader(`{"model":"gpt-5","input":"after raise"}`))
	if err != nil {
		t.Fatalf("post-reload call: %v", err)
	}
	_, _ = io.ReadAll(r3.Body)
	_ = r3.Body.Close()
	if r3.StatusCode != http.StatusOK {
		t.Fatalf("post-reload status = %d, want 200", r3.StatusCode)
	}
	// Total is now 120 + 90 = 210 >= 200; verify manifest updated.
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		loaded, err = host.LoadManifest(id)
		if err == nil && loaded.Usage != nil && loaded.Usage.Total.TotalTokens == 210 && loaded.Usage.Budget == 200 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if loaded == nil || loaded.Usage == nil || loaded.Usage.Total.TotalTokens != 210 || loaded.Usage.Budget != 200 {
		t.Fatalf("Manifest.Usage after reload = %+v, want total=210 budget=200", loaded.Usage)
	}
}

// TestLiveHostAModelBudgetRealAgents verifies on a real Linux host (host-a) that
// real Claude Code and real Codex make EXACTLY ONE request when model_budget is
// reached (zero retries against the 429 refusal) and count tokens on turn 1.
// Opt-in via <BRAND>_LIVE_HOST_TEST=1 and <BRAND>_LIVE_AGENT_BIN_DIR.
func TestLiveHostAModelBudgetRealAgents(t *testing.T) {
	if os.Getenv(brand.Env("LIVE_HOST_TEST")) != "1" {
		t.Skip("set " + brand.Env("LIVE_HOST_TEST") + "=1 to run live real-agent model budget test on host-a")
	}
	binDir := os.Getenv(brand.Env("LIVE_AGENT_BIN_DIR"))
	if binDir == "" {
		home, _ := os.UserHomeDir()
		binDir = filepath.Join(home, ".local", "bin")
	}

	runAgentCase := func(t *testing.T, name, agentBin, routeSlug, upstreamHost string, runTurn func(t *testing.T, gwBaseURL, workDir string) (int, string)) {
		t.Helper()
		fullBin := filepath.Join(binDir, agentBin)
		if _, err := os.Stat(fullBin); err != nil {
			t.Skipf("%s not found at %s", agentBin, fullBin)
		}

		var upstreamHits atomic.Int32
		upSrv, pool := startMockVerifiedUpstream(t, func(w http.ResponseWriter, r *http.Request) {
			upstreamHits.Add(1)
			if r.URL.Path == "/v1/messages" {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w,
					"event: message_start\n"+
						`data: {"type":"message_start","message":{"id":"msg_live_1","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4-6","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":100,"output_tokens":1}}}`+"\n\n"+
						"event: content_block_start\n"+
						`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`+"\n\n"+
						"event: content_block_delta\n"+
						`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"BUDGET_TURN1_OK"}}`+"\n\n"+
						"event: content_block_stop\n"+
						`data: {"type":"content_block_stop","index":0}`+"\n\n"+
						"event: message_delta\n"+
						`data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":50}}`+"\n\n"+
						"event: message_stop\n"+
						`data: {"type":"message_stop"}`+"\n\n")
				return
			}
			if r.URL.Path == "/v1/responses" {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w,
					"event: response.created\n"+
						`data: {"type":"response.created","response":{"id":"resp_live_1","object":"response","status":"in_progress","model":"gpt-5","output":[]}}`+"\n\n"+
						"event: response.output_item.added\n"+
						`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","status":"in_progress","role":"assistant","content":[]}}`+"\n\n"+
						"event: response.output_text.delta\n"+
						`data: {"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"BUDGET_TURN1_OK"}`+"\n\n"+
						"event: response.output_item.done\n"+
						`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"BUDGET_TURN1_OK","annotations":[]}]}}`+"\n\n"+
						"event: response.completed\n"+
						`data: {"type":"response.completed","response":{"id":"resp_live_1","object":"response","status":"completed","model":"gpt-5","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"BUDGET_TURN1_OK","annotations":[]}]}],"usage":{"input_tokens":100,"output_tokens":50,"total_tokens":150}}}`+"\n\n")
				return
			}
			w.WriteHeader(http.StatusNotFound)
		}, upstreamHost)

		ap, err := egress.NewAddressPolicyWithHosts(nil)
		if err != nil {
			t.Fatal(err)
		}
		fakePub := netip.AddrFrom4([4]byte{93, 184, 216, 34})
		res := staticResolver{ips: map[string][]netip.Addr{upstreamHost: {fakePub}}}
		dial := func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, upSrv.Listener.Addr().String())
		}
		eng, err := egress.NewEngine(egress.Config{
			Session: "host-a/" + name,
			Agent:   agentBin,
			Sandbox: egress.SandboxConfig{Preset: egress.PresetBalanced},
			Network: egress.NetworkConfig{ModelBudget: 100},
		})
		if err != nil {
			t.Fatal(err)
		}
		secrets := egress.NewSecretStore()
		header, format := "x-api-key", "{value}"
		if routeSlug == "openai" {
			header, format = "Authorization", "Bearer {value}"
		}
		if err := secrets.Arm([]egress.BoundCredential{
			{Route: routeSlug, Name: "TEST_KEY", Header: header, Format: format, Value: api.Secret("real-test-key")},
		}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(secrets.Close)

		var (
			auditMu      sync.Mutex
			budgetDenied int
		)
		srv, err := egress.NewServer(egress.ServerOptions{
			Engine:     eng,
			Addrs:      ap,
			Dialer:     &egress.SafeDialer{Addrs: ap, Resolver: res, Dial: dial},
			Secrets:    secrets,
			GatewayTLS: &tls.Config{RootCAs: pool},
			Audit: func(ev egress.AuditEvent) {
				if ev.Rule == "model_budget" && ev.Status == http.StatusTooManyRequests {
					auditMu.Lock()
					budgetDenied++
					auditMu.Unlock()
				}
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		gwSrv := httptest.NewServer(srv)
		t.Cleanup(gwSrv.Close)

		workDir := t.TempDir()
		// Turn 1: succeeds under budget (0 < 100) and reports 150 tokens (crossing 100).
		rc1, out1 := runTurn(t, gwSrv.URL, workDir)
		t.Logf("%s turn 1: rc=%d out=%q", name, rc1, strings.TrimSpace(out1))
		if rc1 != 0 || !strings.Contains(out1, "BUDGET_TURN1_OK") {
			t.Fatalf("%s turn 1 failed: rc=%d out=%s", name, rc1, out1)
		}
		snap := srv.Usage.SnapshotWithBudget(eng.ModelBudget())
		if snap.Total.TotalTokens < 150 {
			t.Fatalf("%s usage after turn 1 = %+v, want >= 150", name, snap)
		}
		hitsAfterTurn1 := upstreamHits.Load()

		// Turn 2: budget already reached -> agent must make EXACTLY ONE model request and stop without retry!
		rc2, out2 := runTurn(t, gwSrv.URL, workDir)
		t.Logf("%s turn 2 (over budget): rc=%d out=%q", name, rc2, strings.TrimSpace(out2))
		if upstreamHits.Load() != hitsAfterTurn1 {
			t.Errorf("%s turn 2 reached upstream (%d -> %d)", name, hitsAfterTurn1, upstreamHits.Load())
		}
		auditMu.Lock()
		denied := budgetDenied
		auditMu.Unlock()
		if denied != 1 {
			t.Errorf("%s turn 2 made %d refused model requests, want EXACTLY 1 (zero retries!)", name, denied)
		}
	}

	t.Run("real_claude", func(t *testing.T) {
		runAgentCase(t, "real-claude", "claude", "anthropic", "api.anthropic.com", func(t *testing.T, gwURL, workDir string) (int, string) {
			t.Helper()
			homeDir := t.TempDir()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, filepath.Join(binDir, "claude"), "-p", "reply ok", "--output-format", "text", "--no-session-persistence")
			cmd.Dir = workDir
			cmd.Env = []string{
				"HOME=" + homeDir,
				"PATH=" + binDir + ":/usr/local/bin:/usr/bin:/bin",
				"ANTHROPIC_BASE_URL=" + gwURL + "/gw/anthropic",
				"ANTHROPIC_API_KEY=placeholder-not-real",
				"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
			}
			out, err := cmd.CombinedOutput()
			if err != nil {
				var ee *exec.ExitError
				if ok := errorAs(err, &ee); ok {
					return ee.ExitCode(), string(out)
				}
				return 1, string(out) + " " + err.Error()
			}
			return 0, string(out)
		})
	})

	t.Run("real_codex", func(t *testing.T) {
		runAgentCase(t, "real-codex", "codex", "openai", "api.openai.com", func(t *testing.T, gwURL, workDir string) (int, string) {
			t.Helper()
			homeDir := t.TempDir()
			codexDir := filepath.Join(homeDir, ".codex")
			if err := os.MkdirAll(codexDir, 0o700); err != nil {
				t.Fatal(err)
			}
			cfg := fmt.Sprintf("model_provider = \"gateway\"\n\n[model_providers.gateway]\nname = \"gateway\"\nbase_url = %q\nenv_key = \"OPENAI_API_KEY\"\nwire_api = \"responses\"\nsupports_websockets = false\n", gwURL+"/gw/openai/v1")
			if err := os.WriteFile(filepath.Join(codexDir, "config.toml"), []byte(cfg), 0o600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, filepath.Join(binDir, "codex"), "exec", "--skip-git-repo-check", "reply ok")
			cmd.Dir = workDir
			cmd.Env = []string{
				"HOME=" + homeDir,
				"CODEX_HOME=" + codexDir,
				"PATH=" + binDir + ":/usr/local/bin:/usr/bin:/bin",
				"OPENAI_API_KEY=placeholder-not-real",
			}
			out, err := cmd.CombinedOutput()
			if err != nil {
				var ee *exec.ExitError
				if ok := errorAs(err, &ee); ok {
					return ee.ExitCode(), string(out)
				}
				return 1, string(out) + " " + err.Error()
			}
			return 0, string(out)
		})
	})
}

func errorAs(err error, target any) bool {
	if ee, ok := err.(*exec.ExitError); ok { //nolint:errorlint // direct check for test helper
		if dst, ok := target.(**exec.ExitError); ok {
			*dst = ee
			return true
		}
	}
	return false
}
