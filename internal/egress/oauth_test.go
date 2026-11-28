package egress

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
)

func makeTestJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	raw, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	pay := base64.RawURLEncoding.EncodeToString(raw)
	return hdr + "." + pay + ".test-sig"
}

func startOAuthTestEgress(
	t *testing.T,
	args StartArgs,
	hostAddrs map[string]string,
	pool *x509.CertPool,
) (*Client, *auditRecorder, *http.Client) {
	t.Helper()
	ap, err := NewAddressPolicyWithHosts(nil)
	if err != nil {
		t.Fatal(err)
	}
	ips := map[string][]netip.Addr{}
	ipToAddr := map[string]string{}
	octet := byte(34)
	for h, addr := range hostAddrs {
		pub := ip4(93, 184, 216, octet)
		octet++
		ips[h] = []netip.Addr{pub}
		ipToAddr[pub.String()] = addr
	}
	res := &countingResolver{ips: ips}
	dial := func(ctx context.Context, network, target string) (net.Conn, error) {
		host, _, _ := net.SplitHostPort(target)
		dst := ipToAddr[host]
		if dst == "" {
			for _, a := range hostAddrs {
				dst = a
				break
			}
		}
		return (&net.Dialer{}).DialContext(ctx, network, dst)
	}

	rec := &auditRecorder{}
	supConn, egConn := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		sd := &SafeDialer{Addrs: ap, Resolver: res, Dial: dial}
		done <- RunOnConn(ctx, "oauth-gw-test", egConn, &ServerOptions{
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

	hc := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return cli.OpenEgress(ctx, StreamHeader{Via: "explicit"})
			},
		},
		Timeout: 10 * time.Second,
	}
	return cli, rec, hc
}

// TestClaudeOAuthRecordedHeadersOnGateway replays the exact request shape and
// header set recorded from real claude 2.1.332 with CLAUDE_CODE_OAUTH_TOKEN
// against a local ANTHROPIC_BASE_URL mock upstream.
func TestClaudeOAuthRecordedHeadersOnGateway(t *testing.T) {
	const (
		realOAuth    = "sk-ant-oat01-real-recorded-oauth-token-never-in-sandbox"
		recordedBeta = "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14,context-management-2025-06-27,per-turn-control-2026-07-01,mid-conversation-tool-changes-2026-07-01,thinking-token-count-2026-05-13,advanced-tool-use-2025-11-20,effort-2025-11-24,extended-cache-ttl-2025-04-11"
	)

	var (
		mu      sync.Mutex
		gotPath string
		gotQ    string
		gotHdr  http.Header
	)
	upSrv, pool := startMockModelUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotPath = r.URL.Path
		gotQ = r.URL.RawQuery
		gotHdr = r.Header.Clone()
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_oauth","type":"message","role":"assistant","model":"claude-sonnet-4-6","content":[{"type":"text","text":"PROBE_OK"}],"stop_reason":"end_turn","usage":{"input_tokens":12,"output_tokens":4}}`)
	}, "api.anthropic.com")

	cli, rec, hc := startOAuthTestEgress(t, StartArgs{
		Session:    "devbox/claude-oauth",
		Agent:      "claude",
		PolicyTOML: "[sandbox]\npreset = \"locked\"\n",
		BoundSecrets: []BoundCredential{{
			Route:  "anthropic",
			Name:   "CLAUDE_CODE_OAUTH_TOKEN",
			Header: "Authorization",
			Format: "Bearer {value}",
			Value:  api.Secret(realOAuth),
		}},
	}, map[string]string{"api.anthropic.com": upSrv.Listener.Addr().String()}, pool)

	req, err := http.NewRequest(http.MethodPost, "http://127.0.0.1:3128/gw/anthropic/v1/messages?beta=true",
		strings.NewReader(`{"model":"claude-sonnet-4-6","max_tokens":16,"messages":[{"role":"user","content":"Reply with PROBE_OK"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+brand.Name+"-gateway-placeholder-claude_code_oauth_token")
	req.Header.Set("x-api-key", "sk-ant-api03-planted-attacker-key")
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("anthropic-beta", recordedBeta)
	req.Header.Set("anthropic-dangerous-direct-browser-access", "true")
	req.Header.Set("x-app", "cli")
	req.Header.Set("User-Agent", "claude-cli/2.1.332 (external, sdk-cli)")

	resp, err := hc.Do(req)
	if err != nil {
		t.Fatalf("POST /gw/anthropic/v1/messages?beta=true: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	mu.Lock()
	defer mu.Unlock()
	if gotPath != "/v1/messages" || gotQ != "beta=true" {
		t.Errorf("upstream path/query = %q?%q, want /v1/messages?beta=true", gotPath, gotQ)
	}
	if gotHdr.Get("Authorization") != "Bearer "+realOAuth {
		t.Errorf("upstream Authorization = %q, want Bearer <realOAuth>", gotHdr.Get("Authorization"))
	}
	if gotHdr.Get("X-Api-Key") != "" {
		t.Errorf("planted X-Api-Key leaked upstream: %q", gotHdr.Get("X-Api-Key"))
	}
	if gotHdr.Get("Anthropic-Beta") != recordedBeta {
		t.Errorf("Anthropic-Beta = %q, want verbatim recorded beta header", gotHdr.Get("Anthropic-Beta"))
	}
	if gotHdr.Get("Anthropic-Version") != "2023-06-01" || gotHdr.Get("X-App") != "cli" {
		t.Errorf("preserved headers: version=%q x-app=%q", gotHdr.Get("Anthropic-Version"), gotHdr.Get("X-App"))
	}

	usage, err := cli.Usage(context.Background())
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if usage.Total.TotalTokens != 16 || usage.Total.Requests != 1 {
		t.Errorf("usage = %+v, want total=16 requests=1", usage)
	}
	ev := rec.waitFor(t, func(e AuditEvent) bool {
		return e.Mode == "gateway" && e.Host == "api.anthropic.com" && e.Decision == "allow"
	})
	if ev.Secret != "CLAUDE_CODE_OAUTH_TOKEN" {
		t.Errorf("audit secret = %q, want CLAUDE_CODE_OAUTH_TOKEN", ev.Secret)
	}
	for _, e := range rec.snapshot() {
		raw, _ := json.Marshal(e)
		if strings.Contains(string(raw), realOAuth) {
			t.Fatalf("OAuth token leaked into audit event: %s", raw)
		}
	}
}

func TestCodexChatGPTGatewayGuardAndCredentialSwap(t *testing.T) {
	realAccess := makeTestJWT(t, map[string]any{
		"exp": time.Now().Add(time.Hour).Unix(),
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "acct_real_host_999",
		},
	})
	const (
		realRefresh = "rt_real_host_refresh_never_in_sandbox"
		realIDToken = "id_real_host_token_never_in_sandbox"
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
		if r.URL.Path == "/backend-api/codex/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"models":[{"slug":"gpt-5.4"}]}`)
			return
		}
		if r.URL.Path == "/backend-api/codex/responses" {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-5.4\",\"status\":\"completed\",\"usage\":{\"input_tokens\":30,\"output_tokens\":10,\"total_tokens\":40}}}\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}, "chatgpt.com")

	cli, rec, hc := startOAuthTestEgress(t, StartArgs{
		Session: "devbox/codex-chatgpt",
		Agent:   "codex",
		PolicyTOML: `
[sandbox]
preset = "open"
[network]
allow = ["chatgpt.com"]
`,
		BoundSecrets: []BoundCredential{{
			Route:  "chatgpt",
			Name:   "CODEX_AUTH_JSON",
			Header: "Authorization",
			Format: "Bearer {value}",
			Value:  api.Secret(realAccess),
			OAuth: &OAuthConfig{
				Format:        AuthFormatCodexChatGPT,
				TokenURL:      "https://auth.openai.com/oauth/token",
				ClientID:      "app_EMoamEEZ73f0CkXaXp7hrann",
				AccountHeader: "Chatgpt-Account-Id",
				AccountID:     "acct_real_host_999",
				RefreshToken:  api.Secret(realRefresh),
				IDToken:       api.Secret(realIDToken),
				ExpiresAt:     ParseJWTExpiry(realAccess),
			},
		}},
	}, map[string]string{"chatgpt.com": upSrv.Listener.Addr().String()}, pool)

	// 1. Direct CONNECT chatgpt.com:443 is refused even under preset=open + allow.
	st, err := cli.OpenEgress(context.Background(), StreamHeader{Via: "explicit"})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(st, "CONNECT chatgpt.com:443 HTTP/1.1\r\nHost: chatgpt.com:443\r\n\r\n")
	cresp, err := http.ReadResponse(bufio.NewReader(st), &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("CONNECT chatgpt.com: %v", err)
	}
	cbody, _ := io.ReadAll(cresp.Body)
	_ = cresp.Body.Close()
	_ = st.Close()
	if cresp.StatusCode != http.StatusForbidden || !strings.Contains(string(cbody), "model gateway") {
		t.Fatalf("CONNECT chatgpt.com = %d %q, want 403 model gateway", cresp.StatusCode, cbody)
	}

	// 2. Allowed endpoints: models, responses, compact, input_tokens.
	mreq, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:3128/gw/chatgpt/backend-api/codex/models?client_version=0.172.0", nil)
	mreq.Header.Set("Authorization", "Bearer placeholder-guest-jwt")
	mreq.Header.Set("Chatgpt-Account-Id", "acct_placeholder")
	mresp, err := hc.Do(mreq)
	if err != nil {
		t.Fatalf("GET models: %v", err)
	}
	_, _ = io.ReadAll(mresp.Body)
	_ = mresp.Body.Close()
	if mresp.StatusCode != http.StatusOK {
		t.Fatalf("GET models = %d, want 200", mresp.StatusCode)
	}

	rreq, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:3128/gw/chatgpt/backend-api/codex/responses",
		strings.NewReader(`{"model":"gpt-5.4","input":[{"role":"user","content":"hi"}]}`))
	rreq.Header.Set("Content-Type", "application/json")
	rreq.Header.Set("Authorization", "Bearer placeholder-guest-jwt")
	rreq.Header.Set("Chatgpt-Account-Id", "acct_placeholder")
	rresp, err := hc.Do(rreq)
	if err != nil {
		t.Fatalf("POST responses: %v", err)
	}
	_, _ = io.ReadAll(rresp.Body)
	_ = rresp.Body.Close()
	if rresp.StatusCode != http.StatusOK {
		t.Fatalf("POST responses = %d, want 200", rresp.StatusCode)
	}

	for _, extraPath := range []string{"/backend-api/codex/responses/compact", "/backend-api/codex/responses/input_tokens"} {
		ereq, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:3128/gw/chatgpt"+extraPath, strings.NewReader(`{"model":"gpt-5.4"}`))
		ereq.Header.Set("Content-Type", "application/json")
		eresp, err := hc.Do(ereq)
		if err != nil {
			t.Fatalf("POST %s: %v", extraPath, err)
		}
		_, _ = io.ReadAll(eresp.Body)
		_ = eresp.Body.Close()
		if eresp.StatusCode != http.StatusOK {
			t.Errorf("POST %s = %d, want 200", extraPath, eresp.StatusCode)
		}
	}

	// Verify real Authorization and real Chatgpt-Account-Id reached upstream.
	mu.Lock()
	if len(reqs) < 2 {
		mu.Unlock()
		t.Fatalf("expected >= 2 upstream requests, got %d", len(reqs))
	}
	gotResp := reqs[1]
	mu.Unlock()
	if gotResp.Header.Get("Authorization") != "Bearer "+realAccess {
		t.Errorf("upstream Authorization = %q, want real access token", gotResp.Header.Get("Authorization"))
	}
	if gotResp.Header.Get("Chatgpt-Account-Id") != "acct_real_host_999" {
		t.Errorf("upstream Chatgpt-Account-Id = %q, want acct_real_host_999", gotResp.Header.Get("Chatgpt-Account-Id"))
	}

	// 3. Non-model ChatGPT paths are refused by ModelGuard (403, x-should-retry: false).
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/backend-api/ps/mcp"},
		{http.MethodGet, "/backend-api/wham/accounts/check"},
		{http.MethodPost, "/backend-api/codex/analytics-events/events"},
		{http.MethodPost, "/backend-api/conversation"},
	} {
		breq, _ := http.NewRequest(tc.method, "http://127.0.0.1:3128/gw/chatgpt"+tc.path, strings.NewReader(`{}`))
		bresp, err := hc.Do(breq)
		if err != nil {
			t.Fatalf("%s %s: %v", tc.method, tc.path, err)
		}
		_, _ = io.ReadAll(bresp.Body)
		_ = bresp.Body.Close()
		if bresp.StatusCode != http.StatusForbidden || bresp.Header.Get("x-should-retry") != "false" {
			t.Errorf("%s %s = %d retry=%q, want 403 x-should-retry:false", tc.method, tc.path, bresp.StatusCode, bresp.Header.Get("x-should-retry"))
		}
	}

	// 4. Disallowed server tool (mcp) on /backend-api/codex/responses is refused.
	treq, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:3128/gw/chatgpt/backend-api/codex/responses",
		strings.NewReader(`{"model":"gpt-5.4","tools":[{"type":"mcp","server_url":"https://evil.example"}]}`))
	treq.Header.Set("Content-Type", "application/json")
	tresp, err := hc.Do(treq)
	if err != nil {
		t.Fatalf("POST mcp tool: %v", err)
	}
	_, _ = io.ReadAll(tresp.Body)
	_ = tresp.Body.Close()
	if tresp.StatusCode != http.StatusForbidden || tresp.Header.Get("x-should-retry") != "false" {
		t.Errorf("mcp tool = %d retry=%q, want 403 x-should-retry:false", tresp.StatusCode, tresp.Header.Get("x-should-retry"))
	}

	// 5. Usage counted on /backend-api/codex/responses and no secrets in audit.
	usage, err := cli.Usage(context.Background())
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if usage.Total.TotalTokens != 40 || usage.Total.Requests != 1 {
		t.Errorf("usage = %+v, want total=40 requests=1", usage)
	}
	for _, e := range rec.snapshot() {
		raw, _ := json.Marshal(e)
		for _, sec := range []string{realAccess, realRefresh, realIDToken} {
			if strings.Contains(string(raw), sec) {
				t.Fatalf("OAuth secret leaked into audit: %s", raw)
			}
		}
	}
}

func TestCodexOAuthRefreshProactiveReactiveSingleFlightAndHostWriteBack(t *testing.T) {
	// Token 1: expires in 60s (within 5m proactive refresh skew).
	tok1 := makeTestJWT(t, map[string]any{
		"exp":                         time.Now().Add(60 * time.Second).Unix(),
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct_v1"},
	})
	// Token 2: valid for 1h (returned on proactive refresh).
	tok2 := makeTestJWT(t, map[string]any{
		"exp":                         time.Now().Add(time.Hour).Unix(),
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct_v2"},
	})
	// Token 3: valid for 2h (returned on reactive 401 refresh).
	tok3 := makeTestJWT(t, map[string]any{
		"exp":                         time.Now().Add(2 * time.Hour).Unix(),
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct_v3"},
	})

	const (
		ref1 = "rt_initial_token_1"
		ref2 = "rt_rotated_token_2"
		ref3 = "rt_rotated_token_3"
	)

	// Pre-existing host auth.json with extra user field that must be preserved.
	tmpDir := t.TempDir()
	authPath := filepath.Join(tmpDir, "auth.json")
	lockPath := filepath.Join(tmpDir, "chatgpt.lock")
	initialJSON := fmt.Sprintf(`{"auth_mode":"chatgpt","OPENAI_API_KEY":null,"custom_field":"keep-me","tokens":{"access_token":%q,"refresh_token":%q,"id_token":"id_v1","account_id":"acct_v1"}}`, tok1, ref1)
	if err := os.WriteFile(authPath, []byte(initialJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	var (
		refreshCalls atomic.Int32
		phase        atomic.Int32 // 0=accept tok2, 1=401 on tok2 & accept tok3, 2=fail refresh
		lastRefresh  sync.Mutex
		refreshReqs  []oauthRefreshPayload
	)

	upSrv, pool := startMockModelUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Host == "auth.openai.com" && r.URL.Path == "/oauth/token" {
			n := refreshCalls.Add(1)
			raw, _ := io.ReadAll(r.Body)
			var p oauthRefreshPayload
			_ = json.Unmarshal(raw, &p)
			lastRefresh.Lock()
			refreshReqs = append(refreshReqs, p)
			lastRefresh.Unlock()

			if phase.Load() == 2 {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(w, `{"error":"refresh_token_reused"}`)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if n == 1 {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"access_token":  tok2,
					"refresh_token": ref2,
					"id_token":      "id_v2",
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  tok3,
				"refresh_token": ref3,
				"id_token":      "id_v3",
			})
			return
		}
		// chatgpt.com upstream
		auth := r.Header.Get("Authorization")
		acct := r.Header.Get("Chatgpt-Account-Id")
		wantTok := tok2
		wantAcct := "acct_v2"
		if phase.Load() >= 1 {
			wantTok = tok3
			wantAcct = "acct_v3"
		}
		if auth != "Bearer "+wantTok || acct != wantAcct {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"detail":"token expired"}`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"resp_ok","echo_len":%d,"usage":{"input_tokens":5,"output_tokens":5,"total_tokens":10}}`, len(body))
	}, "chatgpt.com", "auth.openai.com")

	cli, _, hc := startOAuthTestEgress(t, StartArgs{
		Session:    "devbox/codex-refresh",
		Agent:      "codex",
		PolicyTOML: "[sandbox]\npreset = \"locked\"\n",
		BoundSecrets: []BoundCredential{{
			Route:  "chatgpt",
			Name:   "CODEX_AUTH_JSON",
			Header: "Authorization",
			Format: "Bearer {value}",
			Value:  api.Secret(tok1),
			OAuth: &OAuthConfig{
				Format:        AuthFormatCodexChatGPT,
				TokenURL:      "https://auth.openai.com/oauth/token",
				ClientID:      "app_EMoamEEZ73f0CkXaXp7hrann",
				AccountHeader: "Chatgpt-Account-Id",
				AccountID:     "acct_v1",
				RefreshToken:  api.Secret(ref1),
				IDToken:       "id_v1",
				ExpiresAt:     ParseJWTExpiry(tok1),
				AuthFile:      authPath,
				LockFile:      lockPath,
			},
		}},
	}, map[string]string{
		"chatgpt.com":     upSrv.Listener.Addr().String(),
		"auth.openai.com": upSrv.Listener.Addr().String(),
	}, pool)

	// 1. Proactive refresh before expiry + host auth.json atomic write-back.
	reqBody := `{"model":"gpt-5.4","input":"proactive"}`
	resp, err := hc.Post("http://127.0.0.1:3128/gw/chatgpt/backend-api/codex/responses", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("proactive POST: %v", err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(b), `"echo_len":39`) {
		t.Fatalf("proactive response = %d %s", resp.StatusCode, b)
	}
	if refreshCalls.Load() != 1 {
		t.Fatalf("refresh calls = %d, want 1", refreshCalls.Load())
	}
	// Verify host auth.json was updated with tok2/ref2 and kept custom_field.
	diskToks, err := ReadCodexAuthFile(authPath)
	if err != nil {
		t.Fatalf("ReadCodexAuthFile after refresh: %v", err)
	}
	if string(diskToks.AccessToken) != tok2 || string(diskToks.RefreshToken) != ref2 || diskToks.AccountID != "acct_v2" {
		t.Fatalf("disk tokens after refresh = %+v, want tok2/ref2/acct_v2", diskToks)
	}
	rawDisk, _ := os.ReadFile(authPath)
	if !strings.Contains(string(rawDisk), `"custom_field": "keep-me"`) {
		t.Errorf("host auth.json lost unknown field: %s", rawDisk)
	}
	if fi, err := os.Stat(authPath); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("host auth.json mode = %v err=%v, want 0600", fi.Mode().Perm(), err)
	}

	// 2. Reactive 401 refresh-and-retry under single-flight concurrency (8 goroutines).
	phase.Store(1)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := range 8 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			payload := fmt.Sprintf(`{"model":"gpt-5.4","input":"concurrent-%d"}`, idx)
			r, err := hc.Post("http://127.0.0.1:3128/gw/chatgpt/backend-api/codex/responses", "application/json", strings.NewReader(payload))
			if err != nil {
				errs <- err
				return
			}
			body, _ := io.ReadAll(r.Body)
			_ = r.Body.Close()
			if r.StatusCode != http.StatusOK || !strings.Contains(string(body), fmt.Sprintf(`"echo_len":%d`, len(payload))) {
				errs <- fmt.Errorf("worker %d: status=%d body=%s", idx, r.StatusCode, body)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	// All 8 concurrent 401s must trigger exactly ONE additional refresh call!
	if got := refreshCalls.Load(); got != 2 {
		t.Fatalf("refresh calls after 8 concurrent 401s = %d, want 2", got)
	}
	lastRefresh.Lock()
	if len(refreshReqs) != 2 || refreshReqs[0].RefreshToken != ref1 || refreshReqs[0].RefreshReason != "proactive_expiry" ||
		refreshReqs[1].RefreshToken != ref2 || refreshReqs[1].RefreshReason != "unauthorized" || refreshReqs[1].GrantType != "refresh_token" {
		t.Errorf("refresh payloads = %+v", refreshReqs)
	}
	lastRefresh.Unlock()

	// 3. Cross-session flock adoption: simulate another session rotating auth.json on disk,
	// then re-arm with stale tok1 and verify gateway adopts tok3 from disk WITHOUT calling /oauth/token.
	if err := cli.ArmSecrets(context.Background(), []BoundCredential{{
		Route:  "chatgpt",
		Name:   "CODEX_AUTH_JSON",
		Header: "Authorization",
		Format: "Bearer {value}",
		Value:  api.Secret(tok1),
		OAuth: &OAuthConfig{
			Format:        AuthFormatCodexChatGPT,
			TokenURL:      "https://auth.openai.com/oauth/token",
			ClientID:      "app_EMoamEEZ73f0CkXaXp7hrann",
			AccountHeader: "Chatgpt-Account-Id",
			AccountID:     "acct_v1",
			RefreshToken:  api.Secret(ref1),
			ExpiresAt:     ParseJWTExpiry(tok1),
			AuthFile:      authPath,
			LockFile:      lockPath,
		},
	}}); err != nil {
		t.Fatalf("re-arm stale: %v", err)
	}
	resp3, err := hc.Post("http://127.0.0.1:3128/gw/chatgpt/backend-api/codex/responses", "application/json", strings.NewReader(`{"model":"gpt-5.4","input":"adopt"}`))
	if err != nil {
		t.Fatalf("adopt POST: %v", err)
	}
	_, _ = io.ReadAll(resp3.Body)
	_ = resp3.Body.Close()
	if resp3.StatusCode != http.StatusOK {
		t.Fatalf("adopt status = %d, want 200", resp3.StatusCode)
	}
	if got := refreshCalls.Load(); got != 2 {
		t.Fatalf("adopt should not have called /oauth/token, calls = %d", got)
	}

	// 4. Laptop memory-only mode (AuthFile == "") + failed refresh -> non-retriable 401 with Try: hint.
	phase.Store(2)
	if err := os.Remove(authPath); err != nil {
		t.Fatal(err)
	}
	if err := cli.ArmSecrets(context.Background(), []BoundCredential{{
		Route:  "chatgpt",
		Name:   "CODEX_AUTH_JSON",
		Header: "Authorization",
		Format: "Bearer {value}",
		Value:  api.Secret(tok2),
		OAuth: &OAuthConfig{
			Format:        AuthFormatCodexChatGPT,
			TokenURL:      "https://auth.openai.com/oauth/token",
			ClientID:      "app_EMoamEEZ73f0CkXaXp7hrann",
			AccountHeader: "Chatgpt-Account-Id",
			AccountID:     "acct_v2",
			RefreshToken:  api.Secret(ref2),
			ExpiresAt:     time.Now().Add(time.Hour).Unix(),
			// AuthFile empty: memory-only!
		},
	}}); err != nil {
		t.Fatalf("re-arm memory-only: %v", err)
	}
	resp4, err := hc.Post("http://127.0.0.1:3128/gw/chatgpt/backend-api/codex/responses", "application/json", strings.NewReader(`{"model":"gpt-5.4","input":"fail"}`))
	if err != nil {
		t.Fatalf("failed-refresh POST: %v", err)
	}
	body4, _ := io.ReadAll(resp4.Body)
	_ = resp4.Body.Close()
	if resp4.StatusCode != http.StatusUnauthorized || resp4.Header.Get("x-should-retry") != "false" || !strings.Contains(string(body4), "Try:") {
		t.Fatalf("failed refresh = %d retry=%q body=%s", resp4.StatusCode, resp4.Header.Get("x-should-retry"), body4)
	}
	if _, err := os.Stat(authPath); !os.IsNotExist(err) {
		t.Fatalf("memory-only refresh created a file on disk: %v", err)
	}
}

func TestCodexOAuthPolicyDenyBlocksRefreshAndStoreCloseZeroesTokens(t *testing.T) {
	expiredTok := makeTestJWT(t, map[string]any{
		"exp":                         time.Now().Add(-time.Minute).Unix(),
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct_1"},
	})
	var authHit atomic.Bool
	upSrv, pool := startMockModelUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Host == "auth.openai.com" {
			authHit.Store(true)
		}
		w.WriteHeader(http.StatusOK)
	}, "chatgpt.com", "auth.openai.com")

	_, _, hc := startOAuthTestEgress(t, StartArgs{
		Session: "devbox/codex-deny-auth",
		Agent:   "codex",
		PolicyTOML: `
[sandbox]
preset = "locked"
[lock]
deny = ["auth.openai.com"]
`,
		BoundSecrets: []BoundCredential{{
			Route:  "chatgpt",
			Name:   "CODEX_AUTH_JSON",
			Header: "Authorization",
			Format: "Bearer {value}",
			Value:  api.Secret(expiredTok),
			OAuth: &OAuthConfig{
				Format:        AuthFormatCodexChatGPT,
				TokenURL:      "https://auth.openai.com/oauth/token",
				ClientID:      "app_EMoamEEZ73f0CkXaXp7hrann",
				AccountHeader: "Chatgpt-Account-Id",
				AccountID:     "acct_1",
				RefreshToken:  "rt_secret_val",
				ExpiresAt:     ParseJWTExpiry(expiredTok),
			},
		}},
	}, map[string]string{
		"chatgpt.com":     upSrv.Listener.Addr().String(),
		"auth.openai.com": upSrv.Listener.Addr().String(),
	}, pool)

	resp, err := hc.Post("http://127.0.0.1:3128/gw/chatgpt/backend-api/codex/responses", "application/json", strings.NewReader(`{"model":"gpt-5.4"}`))
	if err != nil {
		t.Fatalf("POST with denied auth.openai.com: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(string(body), "denied by policy") {
		t.Fatalf("denied refresh = %d body=%s", resp.StatusCode, body)
	}
	if authHit.Load() {
		t.Fatal("auth.openai.com was contacted despite [lock] deny")
	}

	// Verify SecretStore.RedactMap covers access, refresh and id tokens and Close() zeroes all three.
	st := NewSecretStore()
	if err := st.Arm([]BoundCredential{{
		Route:  "chatgpt",
		Name:   "CODEX_AUTH_JSON",
		Header: "Authorization",
		Format: "Bearer {value}",
		Value:  "acc-secret-12345",
		OAuth: &OAuthConfig{
			Format:        AuthFormatCodexChatGPT,
			TokenURL:      "https://auth.openai.com/oauth/token",
			ClientID:      "app_EMoamEEZ73f0CkXaXp7hrann",
			AccountHeader: "Chatgpt-Account-Id",
			AccountID:     "acct_1",
			RefreshToken:  "ref-secret-67890",
			IDToken:       "id-secret-abcde",
		},
	}}); err != nil {
		t.Fatalf("Arm: %v", err)
	}
	rm := st.RedactMap()
	for name, wantVal := range map[string]string{
		"CODEX_AUTH_JSON":         "acc-secret-12345",
		"CODEX_AUTH_JSON_REFRESH": "ref-secret-67890",
		"CODEX_AUTH_JSON_ID":      "id-secret-abcde",
	} {
		if rm[name] != wantVal {
			t.Errorf("RedactMap[%q] = %q, want %q (full=%+v)", name, rm[name], wantVal, rm)
		}
	}
	sc := st.storedForRoute("chatgpt")
	accBuf, refBuf, idBuf := sc.value, sc.refreshToken, sc.idToken
	st.Close()
	for name, buf := range map[string][]byte{"access": accBuf, "refresh": refBuf, "id": idBuf} {
		for _, b := range buf {
			if b != 0 {
				t.Fatalf("%s token slice not zeroed on Close()", name)
			}
		}
	}
}
