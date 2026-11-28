package egress

import (
	"bufio"
	"bytes"
	"compress/gzip"
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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
)

type recordedUpstreamReq struct {
	Host   string
	Method string
	Path   string
	Header http.Header
	Body   []byte
}

func startMockModelUpstream(t *testing.T, handler http.HandlerFunc, dnsNames ...string) (*httptest.Server, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
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

func startGatewayTestEgress(t *testing.T, args StartArgs, upAddr string, pool *x509.CertPool) (*Client, *auditRecorder, *http.Client) {
	t.Helper()
	ap, err := NewAddressPolicyWithHosts(nil)
	if err != nil {
		t.Fatal(err)
	}
	fakePub := ip4(93, 184, 216, 34)
	res := &countingResolver{ips: map[string][]netip.Addr{
		"api.anthropic.com": {fakePub},
		"api.openai.com":    {fakePub},
	}}
	dial := func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, upAddr)
	}

	rec := &auditRecorder{}
	supConn, egConn := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		sd := &SafeDialer{Addrs: ap, Resolver: res, Dial: dial}
		done <- RunOnConn(ctx, "gw-test", egConn, &ServerOptions{
			Addrs:      ap,
			Dialer:     sd,
			SNITimeout: 2 * time.Second,
			GatewayTLS: &tls.Config{RootCAs: pool},
		})
	}()

	cli, err := NewClient(ctx, supConn, args, rec.add)
	if err != nil {
		cancel()
		_ = supConn.Close()
		_ = egConn.Close()
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		_ = cli.Close()
		<-done
	})

	// Plain-HTTP client speaking directly over KindEgress streams (as guest.Proxy does).
	hc := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return cli.OpenEgress(ctx, StreamHeader{Via: "explicit"})
			},
		},
		Timeout: 5 * time.Second,
	}
	return cli, rec, hc
}

func TestGatewayCredentialSwapAndDirectConnectBlock(t *testing.T) {
	const (
		realAnthropicKey = "sk-ant-api03-real-key-never-in-sandbox"
		realOAuthToken   = "sk-ant-oat01-real-oauth-token-never-in-sandbox"
		realOpenAIKey    = "sk-proj-real-openai-key-never-in-sandbox"
	)

	var (
		mu   sync.Mutex
		reqs []recordedUpstreamReq
	)
	upSrv, pool := startMockModelUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		reqs = append(reqs, recordedUpstreamReq{
			Host: r.Host, Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone(), Body: body,
		})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Host == "api.anthropic.com" {
			_, _ = io.WriteString(w, `{"id":"msg_1","model":"claude-sonnet-4-6","usage":{"input_tokens":100,"cache_read_input_tokens":50,"output_tokens":25}}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"resp_1","model":"gpt-5","usage":{"input_tokens":80,"output_tokens":20,"total_tokens":100}}`)
	}, "api.anthropic.com", "api.openai.com")

	cli, rec, hc := startGatewayTestEgress(t, StartArgs{
		Session: "devbox/auth-fix",
		PolicyTOML: `
[sandbox]
preset = "open"
[network]
allow = ["api.anthropic.com", "api.openai.com"]
`,
		BoundSecrets: []BoundCredential{
			{Route: "anthropic", Name: "ANTHROPIC_API_KEY", Header: "x-api-key", Format: "{value}", Value: api.Secret(realAnthropicKey)},
			{Route: "openai", Name: "OPENAI_API_KEY", Header: "Authorization", Format: "Bearer {value}", Value: api.Secret(realOpenAIKey)},
		},
	}, upSrv.Listener.Addr().String(), pool)

	// 1. Direct CONNECT to model hosts is ALWAYS refused even under preset=open + explicit allow.
	for _, host := range []string{"api.anthropic.com:443", "api.openai.com:443"} {
		st, err := cli.OpenEgress(context.Background(), StreamHeader{Via: "explicit"})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(st, "CONNECT "+host+" HTTP/1.1\r\nHost: "+host+"\r\n\r\n")
		resp, err := http.ReadResponse(bufio.NewReader(st), &http.Request{Method: http.MethodConnect})
		if err != nil {
			t.Fatalf("CONNECT %s read response: %v", host, err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		_ = st.Close()
		if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "model gateway") {
			t.Errorf("CONNECT %s = %d %q, want 403 mentioning model gateway", host, resp.StatusCode, body)
		}
	}

	// 2. Claude Code API key request: guest's planted creds are stripped and real key injected.
	req, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:3128/gw/anthropic/v1/messages",
		strings.NewReader(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", "sk-ant-planted-attacker-key")
	req.Header.Set("Authorization", "Bearer planted-bearer")
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatalf("POST anthropic gateway: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("anthropic status = %d, want 200", resp.StatusCode)
	}

	mu.Lock()
	gotAnthropic := reqs[0]
	mu.Unlock()
	if gotAnthropic.Host != "api.anthropic.com" || gotAnthropic.Path != "/v1/messages" {
		t.Errorf("upstream target = %s%s", gotAnthropic.Host, gotAnthropic.Path)
	}
	if gotAnthropic.Header.Get("X-Api-Key") != realAnthropicKey {
		t.Errorf("upstream x-api-key = %q, want real key", gotAnthropic.Header.Get("X-Api-Key"))
	}
	if gotAnthropic.Header.Get("Authorization") != "" {
		t.Errorf("upstream Authorization was not stripped: %q", gotAnthropic.Header.Get("Authorization"))
	}
	if gotAnthropic.Header.Get("Anthropic-Beta") != "oauth-2025-04-20" {
		t.Errorf("anthropic-beta not preserved: %q", gotAnthropic.Header.Get("Anthropic-Beta"))
	}

	// 3. Re-arm Anthropic route with OAuth token (Authorization: Bearer + anthropic-beta preserved).
	if err := cli.ArmSecrets(context.Background(), []BoundCredential{{
		Route: "anthropic", Name: "CLAUDE_CODE_OAUTH_TOKEN", Header: "Authorization", Format: "Bearer {value}", Value: api.Secret(realOAuthToken),
	}}); err != nil {
		t.Fatalf("ArmSecrets: %v", err)
	}
	req2, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:3128/gw/anthropic/v1/messages",
		strings.NewReader(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"oauth"}]}`))
	req2.Header.Set("x-api-key", "planted-key")
	req2.Header.Set("anthropic-beta", "oauth-2025-04-20")
	resp2, err := hc.Do(req2)
	if err != nil {
		t.Fatalf("POST anthropic oauth: %v", err)
	}
	_, _ = io.ReadAll(resp2.Body)
	_ = resp2.Body.Close()
	mu.Lock()
	gotOAuth := reqs[1]
	mu.Unlock()
	if gotOAuth.Header.Get("Authorization") != "Bearer "+realOAuthToken || gotOAuth.Header.Get("X-Api-Key") != "" {
		t.Errorf("oauth headers = Auth:%q X-Api-Key:%q", gotOAuth.Header.Get("Authorization"), gotOAuth.Header.Get("X-Api-Key"))
	}

	// 4. Codex / OpenAI Responses request: planted bearer stripped, real OpenAI key injected.
	req3, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:3128/gw/openai/v1/responses",
		strings.NewReader(`{"model":"gpt-5","input":"hello"}`))
	req3.Header.Set("Authorization", "Bearer sk-planted-openai")
	resp3, err := hc.Do(req3)
	if err != nil {
		t.Fatalf("POST openai gateway: %v", err)
	}
	_, _ = io.ReadAll(resp3.Body)
	_ = resp3.Body.Close()
	if resp3.StatusCode != http.StatusOK {
		t.Fatalf("openai status = %d, want 200", resp3.StatusCode)
	}
	mu.Lock()
	gotOpenAI := reqs[2]
	mu.Unlock()
	if gotOpenAI.Host != "api.openai.com" || gotOpenAI.Header.Get("Authorization") != "Bearer "+realOpenAIKey {
		t.Errorf("openai upstream = host %q auth %q", gotOpenAI.Host, gotOpenAI.Header.Get("Authorization"))
	}

	// 5. Verify usage accumulated across both providers and audit never contains secret values.
	summary, err := cli.Usage(context.Background())
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	// 2 Anthropic requests * 175 + 1 OpenAI request * 100 = 450 total tokens.
	if summary.Total.TotalTokens != 450 || summary.Total.Requests != 3 {
		t.Errorf("usage summary = %+v, want total=450 requests=3", summary)
	}
	ev := rec.waitFor(t, func(e AuditEvent) bool {
		return e.Mode == "gateway" && e.Host == "api.openai.com" && e.Decision == "allow"
	})
	if ev.Secret != "OPENAI_API_KEY" || ev.Usage == nil || ev.Usage.TotalTokens != 100 || ev.UsageKind != "reported" {
		t.Errorf("openai audit event = %+v", ev)
	}
	for _, e := range rec.snapshot() {
		raw, _ := json.Marshal(e)
		for _, secret := range []string{realAnthropicKey, realOAuthToken, realOpenAIKey} {
			if strings.Contains(string(raw), secret) {
				t.Fatalf("secret value leaked into audit event: %s", raw)
			}
		}
	}
}

func TestGatewayUnarmedAndWebSocketAndLockRefusal(t *testing.T) {
	upSrv, pool := startMockModelUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("upstream should not have been dialled")
		w.WriteHeader(http.StatusOK)
	}, "api.anthropic.com", "api.openai.com")

	cli, _, hc := startGatewayTestEgress(t, StartArgs{
		Session:    "devbox/s1",
		PolicyTOML: "[sandbox]\npreset = \"balanced\"\n[lock]\ndeny = [\"api.anthropic.com\"]\n",
	}, upSrv.Listener.Addr().String(), pool)

	// 1. Locked upstream host is refused even on the gateway route.
	resp, err := hc.Post("http://127.0.0.1:3128/gw/anthropic/v1/messages", "application/json",
		strings.NewReader(`{"model":"claude-sonnet-4-6","messages":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || resp.Header.Get("x-should-retry") != "false" {
		t.Errorf("locked anthropic gateway = %d retry=%q, want 403/false", resp.StatusCode, resp.Header.Get("x-should-retry"))
	}

	// 2. Unarmed route fails closed with 401 and x-should-retry: false.
	resp, err = hc.Post("http://127.0.0.1:3128/gw/openai/v1/responses", "application/json",
		strings.NewReader(`{"model":"gpt-5","input":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("x-should-retry") != "false" {
		t.Errorf("unarmed openai gateway = %d retry=%q, want 401/false", resp.StatusCode, resp.Header.Get("x-should-retry"))
	}

	// 3. WebSocket upgrade on gateway is refused with 403 and x-should-retry: false.
	wsReq, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:3128/gw/openai/v1/responses", nil)
	wsReq.Header.Set("Connection", "Upgrade")
	wsReq.Header.Set("Upgrade", "websocket")
	resp, err = hc.Do(wsReq)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || resp.Header.Get("x-should-retry") != "false" {
		t.Errorf("websocket upgrade = %d retry=%q, want 403/false", resp.StatusCode, resp.Header.Get("x-should-retry"))
	}
	_ = cli
}

func TestModelGuardRefusalsAndGrants(t *testing.T) {
	upSrv, pool := startMockModelUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"ok","model":"test","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
	}, "api.anthropic.com", "api.openai.com")

	cli, _, hc := startGatewayTestEgress(t, StartArgs{
		Session:    "devbox/auth-fix",
		PolicyTOML: "[sandbox]\npreset = \"locked\"\n",
		BoundSecrets: []BoundCredential{
			{Route: "anthropic", Name: "ANTHROPIC_API_KEY", Header: "x-api-key", Value: "k1"},
			{Route: "openai", Name: "OPENAI_API_KEY", Header: "Authorization", Format: "Bearer {value}", Value: "k2"},
		},
	}, upSrv.Listener.Addr().String(), pool)

	refused := []struct {
		name     string
		url      string
		body     string
		wire     string
		wantSub  string
		gzipBody bool
	}{
		{"anthropic web_fetch", "http://127.0.0.1:3128/gw/anthropic/v1/messages", `{"tools":[{"type":"web_fetch_20260318","name":"wf"}]}`, WireAnthropicMessages, "--tools web_fetch", false},
		{"anthropic code_execution", "http://127.0.0.1:3128/gw/anthropic/v1/messages", `{"tools":[{"type":"code_execution_20260521","name":"ce"}]}`, WireAnthropicMessages, "--tools code_execution", false},
		{"anthropic mcp_toolset", "http://127.0.0.1:3128/gw/anthropic/v1/messages", `{"tools":[{"type":"mcp_toolset","mcp_server_name":"s"}]}`, WireAnthropicMessages, "--tools mcp", false},
		{"anthropic mcp_servers", "http://127.0.0.1:3128/gw/anthropic/v1/messages", `{"mcp_servers":[{"type":"url","url":"https://evil.example","name":"e"}]}`, WireAnthropicMessages, "--tools mcp", false},
		{"anthropic unknown future tool", "http://127.0.0.1:3128/gw/anthropic/v1/messages", `{"tools":[{"type":"web_fetch_20990101","name":"future"}]}`, WireAnthropicMessages, "web_fetch_20990101", false},
		{"anthropic url image", "http://127.0.0.1:3128/gw/anthropic/v1/messages", `{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","url":"https://evil.example/a.png"}}]}]}`, WireAnthropicMessages, "--tools url_content", false},
		{"anthropic url document in tool_result", "http://127.0.0.1:3128/gw/anthropic/v1/messages", `{"messages":[{"role":"user","content":[{"type":"tool_result","content":[{"type":"document","source":{"type":"url","url":"https://evil.example/a.pdf"}}]}]}]}`, WireAnthropicMessages, "--tools url_content", false},
		{"anthropic web_search under locked", "http://127.0.0.1:3128/gw/anthropic/v1/messages", `{"tools":[{"type":"web_search_20260318","name":"ws"}]}`, WireAnthropicMessages, "--tools web_search", false},
		{"anthropic batches path", "http://127.0.0.1:3128/gw/anthropic/v1/messages/batches", `{"requests":[]}`, WireAnthropicMessages, "/v1/messages/batches", false},
		{"anthropic files upload", "http://127.0.0.1:3128/gw/anthropic/v1/files", `{}`, WireAnthropicMessages, "--tools files", false},
		{"anthropic gzip smuggled web_fetch", "http://127.0.0.1:3128/gw/anthropic/v1/messages", `{"tools":[{"type":"web_fetch_20260318","name":"wf"}]}`, WireAnthropicMessages, "--tools web_fetch", true},
		{"anthropic path traversal", "http://127.0.0.1:3128/gw/anthropic/v1/messages/..%2ffiles", `{}`, WireAnthropicMessages, "invalid model API path", false},
		{"openai mcp server_url", "http://127.0.0.1:3128/gw/openai/v1/responses", `{"tools":[{"type":"mcp","server_url":"https://evil.example"}]}`, WireOpenAIResponses, "--tools mcp", false},
		{"openai nested mcp in namespace", "http://127.0.0.1:3128/gw/openai/v1/responses", `{"tools":[{"type":"namespace","name":"multi","tools":[{"type":"mcp","server_url":"https://evil.example"}]}]}`, WireOpenAIResponses, "--tools mcp", false},
		{"openai mcp connector_id", "http://127.0.0.1:3128/gw/openai/v1/responses", `{"tools":[{"type":"mcp","connector_id":"conn_1"}]}`, WireOpenAIResponses, "--tools mcp", false},
		{"openai mcp tunnel_id", "http://127.0.0.1:3128/gw/openai/v1/responses", `{"tools":[{"type":"mcp","tunnel_id":"tun_1"}]}`, WireOpenAIResponses, "--tools mcp", false},
		{"openai code_interpreter", "http://127.0.0.1:3128/gw/openai/v1/responses", `{"tools":[{"type":"code_interpreter"}]}`, WireOpenAIResponses, "--tools code_execution", false},
		{"openai file_search", "http://127.0.0.1:3128/gw/openai/v1/responses", `{"tools":[{"type":"file_search"}]}`, WireOpenAIResponses, "file_search", false},
		{"openai web_search under locked", "http://127.0.0.1:3128/gw/openai/v1/responses", `{"tools":[{"type":"web_search_2025_08_26"}]}`, WireOpenAIResponses, "--tools web_search", false},
		{"openai input_image url", "http://127.0.0.1:3128/gw/openai/v1/responses", `{"input":[{"role":"user","content":[{"type":"input_image","image_url":"https://evil.example/x.png"}]}]}`, WireOpenAIResponses, "--tools url_content", false},
		{"openai input_file url", "http://127.0.0.1:3128/gw/openai/v1/responses", `{"input":[{"role":"user","content":[{"type":"input_file","file_url":"https://evil.example/x.pdf"}]}]}`, WireOpenAIResponses, "--tools url_content", false},
		{"openai batches path", "http://127.0.0.1:3128/gw/openai/v1/batches", `{}`, WireOpenAIResponses, "/v1/batches", false},
		{"openai files upload", "http://127.0.0.1:3128/gw/openai/v1/files", `{}`, WireOpenAIResponses, "--tools files", false},
	}

	for _, tc := range refused {
		t.Run(tc.name, func(t *testing.T) {
			var rdr io.Reader = strings.NewReader(tc.body)
			if tc.gzipBody {
				var buf bytes.Buffer
				gz := gzip.NewWriter(&buf)
				_, _ = gz.Write([]byte(tc.body))
				_ = gz.Close()
				rdr = &buf
			}
			req, err := http.NewRequest(http.MethodPost, tc.url, rdr)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			if tc.gzipBody {
				req.Header.Set("Content-Encoding", "gzip")
			}
			resp, err := hc.Do(req)
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			raw, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("status = %d, want 403; body=%s", resp.StatusCode, raw)
			}
			if resp.Header.Get("x-should-retry") != "false" {
				t.Errorf("x-should-retry = %q, want false", resp.Header.Get("x-should-retry"))
			}
			if resp.Header.Get("Retry-After") != "" {
				t.Errorf("Retry-After must be empty, got %q", resp.Header.Get("Retry-After"))
			}
			var decoded map[string]any
			if err := json.Unmarshal(raw, &decoded); err != nil {
				t.Fatalf("error body is not JSON: %v (%s)", err, raw)
			}
			errObj, _ := decoded["error"].(map[string]any)
			msg, _ := errObj["message"].(string)
			if !strings.Contains(msg, tc.wantSub) || !strings.Contains(msg, "Try:") {
				t.Errorf("error message = %q, want %q and Try:", msg, tc.wantSub)
			}
			if tc.wire == WireAnthropicMessages {
				if decoded["type"] != "error" || errObj["type"] != "permission_error" {
					t.Errorf("unexpected Anthropic error shape: %s", raw)
				}
			} else {
				if errObj["type"] != "request_forbidden" {
					t.Errorf("unexpected OpenAI error shape: %s", raw)
				}
			}
		})
	}

	// Allowed client tools, data: URLs and count_tokens pass under locked.
	allowed := []struct {
		url  string
		body string
	}{
		{"http://127.0.0.1:3128/gw/anthropic/v1/messages", `{"tools":[{"name":"Read","input_schema":{}},{"type":"custom","name":"Edit"}],"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","data":"AA=="}}]}]}`},
		{"http://127.0.0.1:3128/gw/anthropic/v1/messages/count_tokens", `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}]}`},
		{"http://127.0.0.1:3128/gw/openai/v1/responses", `{"tools":[{"type":"function","name":"shell"},{"type":"namespace","name":"multi_agent_v1","tools":[{"type":"function","name":"wait_agent"}]}],"input":[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,AA=="}]}]}`},
	}
	for _, tc := range allowed {
		resp, err := hc.Post(tc.url, "application/json", strings.NewReader(tc.body))
		if err != nil {
			t.Fatalf("allowed POST %s: %v", tc.url, err)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("allowed POST %s = %d %s, want 200", tc.url, resp.StatusCode, b)
		}
	}

	// Live AllowTools opens web_fetch and web_search under locked, and balanced opens web_search automatically.
	if err := cli.AllowTools(context.Background(), "api.anthropic.com", []string{ToolGrantWebFetch, ToolGrantWebSearch}); err != nil {
		t.Fatalf("AllowTools: %v", err)
	}
	resp, err := hc.Post("http://127.0.0.1:3128/gw/anthropic/v1/messages", "application/json",
		strings.NewReader(`{"tools":[{"type":"web_fetch_20260318","name":"wf"},{"type":"web_search_20260318","name":"ws"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("granted web_fetch+web_search status = %d, want 200", resp.StatusCode)
	}
	if err := cli.Reload(context.Background(), "[sandbox]\npreset = \"balanced\"\n"); err != nil {
		t.Fatalf("Reload balanced: %v", err)
	}
	resp, err = hc.Post("http://127.0.0.1:3128/gw/openai/v1/responses", "application/json",
		strings.NewReader(`{"tools":[{"type":"web_search_2025_08_26"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("openai web_search under balanced = %d, want 200", resp.StatusCode)
	}
}

func TestSecretStoreCloseZerosMemory(t *testing.T) {
	st := NewSecretStore()
	if err := st.Arm([]BoundCredential{{
		Route: "anthropic", Name: "ANTHROPIC_API_KEY", Header: "x-api-key", Value: api.Secret("secret-bytes-123"),
	}}); err != nil {
		t.Fatal(err)
	}
	ref := st.creds["anthropic"].value
	st.Close()
	for i, b := range ref {
		if b != 0 {
			t.Fatalf("secret byte %d was not zeroed after Close: %d", i, b)
		}
	}
	if _, _, _, ok := st.Lookup("anthropic"); ok {
		t.Fatal("Lookup succeeded after Close")
	}
}

func FuzzModelGuardJSON(f *testing.F) {
	f.Add([]byte(`{"tools":[{"type":"web_fetch_20260318"}]}`))
	f.Add([]byte(`{"mcp_servers":[{"type":"url","url":"https://x"}]}`))
	f.Add([]byte(`{"input":[{"type":"input_image","image_url":"https://x/a.png"}]}`))
	f.Add([]byte(`not-json`))
	f.Fuzz(func(_ *testing.T, data []byte) {
		for _, wire := range []string{WireAnthropicMessages, WireOpenAIResponses} {
			g := &agents.ModelGuard{Wire: wire, Tools: []string{"custom", "function"}, Search: []string{"web_search"}}
			_ = checkModelBody(g, PresetLocked, data, nil)
		}
	})
}
