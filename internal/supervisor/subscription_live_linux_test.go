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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/guest"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/wall/process"
)

func makeLiveJWT(t *testing.T, exp int64, accountID string) string {
	t.Helper()
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	raw, err := json.Marshal(map[string]any{
		"sub": "user-live-test",
		"exp": exp,
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": accountID,
			"chatgpt_plan_type":  "plus",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return hdr + "." + base64.RawURLEncoding.EncodeToString(raw) + ".live-test-sig"
}

func startMockSubscriptionUpstream(t *testing.T, handler http.HandlerFunc, dnsNames []string) *mockGatewayUpstream {
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
			Path:   r.URL.RequestURI(),
			Header: r.Header.Clone(),
			Body:   string(body),
		})
		m.mu.Unlock()
		r.Body = io.NopCloser(bytes.NewReader(body))
		handler(w, r)
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

// TestLiveHostASubscriptionLoginsRealAgents proves both real Claude Code with
// CLAUDE_CODE_OAUTH_TOKEN and real Codex with ChatGPT host sign-in + OAuth
// refresh through the real supervisor + bubblewrap process wall + wall-init +
// egress gateway against a local verified-TLS mock upstream. Opt-in via
// brand.Env("LIVE_HOST_TEST")=1.
func TestLiveHostASubscriptionLoginsRealAgents(t *testing.T) {
	if os.Getenv(brand.Env("LIVE_HOST_TEST")) != "1" {
		t.Skip("set " + brand.Env("LIVE_HOST_TEST") + "=1 to run live real-agent subscription proof")
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

	dumpPy := `python3 -c 'import os, glob
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
print("DUMP_OK=1")'`

	t.Run("real_claude_oauth", func(t *testing.T) {
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
		resolved, err := filepath.EvalSymlinks(claudeBin)
		if err != nil {
			t.Fatal(err)
		}
		guestBin := "/opt/agents/bin/claude"
		realOAuth := randomHexSecret(t, "sk-ant-oat01-LIVEOAUTH-")

		mock := startMockSubscriptionUpstream(t, func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			writeMockAnthropicResponse(w, r, body)
		}, []string{"api.anthropic.com"})

		claudeRow, _ := agents.Find("claude")
		id := "lvcl101a"
		shellCmd := fmt.Sprintf(
			`%s -p 'Say hello' --output-format text --no-session-persistence --tools '' </dev/null; rc=$?; echo "AGENT_RC=$rc"; env | grep -E '^(ANTHROPIC_|CLAUDE_CODE_)' || true; %s; echo "LIVE_SUB_DONE"; sleep 30`,
			guestBin, dumpPy,
		)
		now := time.Now().UTC()
		m := &host.Manifest{
			Schema:    1,
			ID:        id,
			Name:      "live-claude-oauth",
			CreatedAt: now,
			Agent:     host.AgentInfo{Kind: "claude", Argv: []string{"/bin/sh", "-c", shellCmd}},
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
			},
			Term:    host.TermInfo{Term: "xterm-256color", Cols: 120, Rows: 30},
			Run:     host.Runtime{Socket: paths.Socket(id), BootID: host.BootID()},
			Status:  host.Status{State: host.StateStarting, Since: now},
			Secrets: []string{"CLAUDE_CODE_OAUTH_TOKEN"},
			Wall: &host.WallRecord{
				Level:          "process",
				Driver:         process.DriverName,
				Version:        probe.Version,
				Preset:         string(egress.PresetBalanced),
				WorkspaceHost:  wsHost,
				WorkspaceGuest: "/work/repo",
				StateDirs:      claudeRow.StateDirs,
				ExtraRO:        []host.WallMount{{Source: resolved, Dest: guestBin, ReadOnly: true}},
				BoundSecrets: []api.SecretBind{{
					Name:   "CLAUDE_CODE_OAUTH_TOKEN",
					Hosts:  []string{"api.anthropic.com"},
					Header: "Authorization",
					Format: "Bearer {value}",
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
				HeartbeatTimeout: 15 * time.Second,
				TickInterval:     200 * time.Millisecond,
				StopGrace:        3 * time.Second,
				Egress:           egClient,
			})
		}()

		deliverSecretsToSupervisor(t, id, map[string]api.Secret{"CLAUDE_CODE_OAUTH_TOKEN": api.Secret(realOAuth)})
		waitForSupervisorReady(t, id)
		out := waitForOutput(t, id, "LIVE_SUB_DONE")
		t.Logf("real_claude_oauth output:\n%s", out)

		if !strings.Contains(out, "AGENT_RC=0") || !strings.Contains(out, "hello from mock anthropic gateway") {
			t.Fatalf("claude oauth failed:\n%s", out)
		}
		if !strings.Contains(out, "CLAUDE_CODE_OAUTH_TOKEN="+guest.GatewayPlaceholder("CLAUDE_CODE_OAUTH_TOKEN")) {
			t.Fatalf("missing placeholder CLAUDE_CODE_OAUTH_TOKEN:\n%s", out)
		}
		dumpBytes, err := os.ReadFile(filepath.Join(wsHost, "sandbox-dump.bin"))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(dumpBytes, []byte(realOAuth)) {
			t.Fatal("real CLAUDE_CODE_OAUTH_TOKEN leaked into sandbox!")
		}

		var sawOAuth bool
		for _, req := range mock.requests() {
			if strings.HasPrefix(req.Path, "/v1/messages") {
				t.Logf("claude upstream request: %s %s Auth-match=%v X-Api-Key=%q Beta=%q",
					req.Method, req.Path,
					req.Header.Get("Authorization") == "Bearer "+realOAuth,
					req.Header.Get("X-Api-Key"),
					req.Header.Get("Anthropic-Beta"))
				if req.Header.Get("Authorization") == "Bearer "+realOAuth &&
					req.Header.Get("X-Api-Key") == "" &&
					strings.Contains(req.Header.Get("Anthropic-Beta"), "oauth-2025-04-20") {
					sawOAuth = true
				}
			}
		}
		if !sawOAuth {
			t.Fatalf("upstream never saw real Bearer OAuth token with oauth-2025-04-20 and empty X-Api-Key")
		}
		assertNoSecretOnDisk(t, realOAuth, paths.SessionDir(id), paths.RuntimeDir())

		_, _ = host.HolderRequest(id, "kill", nil, 3*time.Second)
		<-supDone
	})

	t.Run("real_codex_chatgpt_host_signin_and_refresh", func(t *testing.T) {
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
		resolved, err := filepath.EvalSymlinks(codexBin)
		if err != nil {
			t.Fatal(err)
		}
		guestBin := "/opt/agents/bin/codex"

		initAcct := "acct_live_host_v1"
		rotAcct := "acct_live_host_v2"
		initAccess := makeLiveJWT(t, time.Now().Add(-2*time.Minute).Unix(), initAcct)
		rotAccess := makeLiveJWT(t, time.Now().Add(time.Hour).Unix(), rotAcct)
		rotID := makeLiveJWT(t, time.Now().Add(time.Hour).Unix(), rotAcct)
		initRefresh := randomHexSecret(t, "rt_live_init_")
		rotRefresh := randomHexSecret(t, "rt_live_rot_")

		// Seed fake pre-existing host ~/.codex/auth.json (mode 0600).
		hostCodexDir := filepath.Join(subTmp, ".codex")
		if err := os.MkdirAll(hostCodexDir, 0o700); err != nil {
			t.Fatal(err)
		}
		hostAuthPath := filepath.Join(hostCodexDir, "auth.json")
		hostAuthJSON := fmt.Sprintf(`{"auth_mode":"chatgpt","OPENAI_API_KEY":null,"tokens":{"access_token":%q,"refresh_token":%q,"id_token":%q,"account_id":%q}}`,
			initAccess, initRefresh, initAccess, initAcct)
		if err := os.WriteFile(hostAuthPath, []byte(hostAuthJSON), 0o600); err != nil {
			t.Fatal(err)
		}

		var (
			refreshCount atomic.Int32
			mu           sync.Mutex
			sawRefresh   bool
			sawResp      bool
		)
		mock := startMockSubscriptionUpstream(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Host == "auth.openai.com" && r.URL.Path == "/oauth/token" {
				refreshCount.Add(1)
				var payload struct {
					ClientID     string `json:"client_id"`
					GrantType    string `json:"grant_type"`
					RefreshToken string `json:"refresh_token"`
				}
				_ = json.NewDecoder(r.Body).Decode(&payload)
				if payload.GrantType == "refresh_token" && payload.RefreshToken == initRefresh {
					mu.Lock()
					sawRefresh = true
					mu.Unlock()
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"access_token":  rotAccess,
					"refresh_token": rotRefresh,
					"id_token":      rotID,
				})
				return
			}
			if r.Header.Get("Authorization") != "Bearer "+rotAccess || r.Header.Get("Chatgpt-Account-Id") != rotAcct {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(w, `{"detail":"expected rotated token"}`)
				return
			}
			if r.URL.Path == "/backend-api/codex/models" {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"models":[]}`)
				return
			}
			if r.URL.Path == "/backend-api/codex/responses" {
				mu.Lock()
				sawResp = true
				mu.Unlock()
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w,
					"event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_sub\",\"status\":\"in_progress\",\"model\":\"gpt-5.4\"}}\n\n"+
						"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"msg_sub\",\"type\":\"message\",\"status\":\"in_progress\",\"role\":\"assistant\",\"content\":[]}}\n\n"+
						"event: response.content_part.added\ndata: {\"type\":\"response.content_part.added\",\"item_id\":\"msg_sub\",\"output_index\":0,\"content_index\":0,\"part\":{\"type\":\"output_text\",\"text\":\"\"}}\n\n"+
						"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_sub\",\"output_index\":0,\"content_index\":0,\"delta\":\"hello from mock chatgpt subscription gateway\"}\n\n"+
						"event: response.output_text.done\ndata: {\"type\":\"response.output_text.done\",\"item_id\":\"msg_sub\",\"output_index\":0,\"content_index\":0,\"text\":\"hello from mock chatgpt subscription gateway\"}\n\n"+
						"event: response.content_part.done\ndata: {\"type\":\"response.content_part.done\",\"item_id\":\"msg_sub\",\"output_index\":0,\"content_index\":0,\"part\":{\"type\":\"output_text\",\"text\":\"hello from mock chatgpt subscription gateway\"}}\n\n"+
						"event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"msg_sub\",\"type\":\"message\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello from mock chatgpt subscription gateway\"}]}}\n\n"+
						"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_sub\",\"status\":\"completed\",\"model\":\"gpt-5.4\",\"output\":[{\"id\":\"msg_sub\",\"type\":\"message\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello from mock chatgpt subscription gateway\"}]}],\"usage\":{\"input_tokens\":18,\"output_tokens\":7,\"total_tokens\":25}}}\n\n",
				)
				return
			}
			w.WriteHeader(http.StatusNotFound)
		}, []string{"chatgpt.com", "auth.openai.com"})

		codexRow, _ := agents.Find("codex")
		id := "lvcx101b"
		mcpProbe := `python3 -c 'import urllib.request, urllib.error
try:
    urllib.request.urlopen(urllib.request.Request("http://127.0.0.1:3128/gw/chatgpt/backend-api/ps/mcp", data=b"{}", method="POST"), timeout=5)
    print("MCP_STATUS=200")
except urllib.error.HTTPError as e:
    print("MCP_STATUS=" + str(e.code))'`
		shellCmd := fmt.Sprintf(
			`%s exec --strict-config --skip-git-repo-check -m gpt-5.4 'Say hello' </dev/null; rc=$?; echo "AGENT_RC=$rc"; %s; %s; echo "LIVE_SUB_DONE"; sleep 30`,
			guestBin, mcpProbe, dumpPy,
		)
		now := time.Now().UTC()
		m := &host.Manifest{
			Schema:    1,
			ID:        id,
			Name:      "live-codex-chatgpt",
			CreatedAt: now,
			Agent:     host.AgentInfo{Kind: "codex", Argv: []string{"/bin/sh", "-c", shellCmd}},
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
			},
			Term:   host.TermInfo{Term: "xterm-256color", Cols: 120, Rows: 30},
			Run:    host.Runtime{Socket: paths.Socket(id), BootID: host.BootID()},
			Status: host.Status{State: host.StateStarting, Since: now},
			Wall: &host.WallRecord{
				Level:            "process",
				Driver:           process.DriverName,
				Version:          probe.Version,
				Preset:           string(egress.PresetBalanced),
				WorkspaceHost:    wsHost,
				WorkspaceGuest:   "/work/repo",
				StateDirs:        codexRow.StateDirs,
				ExtraRO:          []host.WallMount{{Source: resolved, Dest: guestBin, ReadOnly: true}},
				SubscriptionAuth: "host",
				At:               now,
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
				HeartbeatTimeout: 15 * time.Second,
				TickInterval:     200 * time.Millisecond,
				StopGrace:        3 * time.Second,
				Egress:           egClient,
			})
		}()

		waitForSupervisorReady(t, id)
		out := waitForOutput(t, id, "LIVE_SUB_DONE")
		t.Logf("real_codex_chatgpt output:\n%s", out)

		if !strings.Contains(out, "AGENT_RC=0") || !strings.Contains(out, "hello from mock chatgpt subscription gateway") {
			t.Fatalf("codex chatgpt failed:\n%s", out)
		}
		if !strings.Contains(out, "MCP_STATUS=403") {
			t.Fatalf("expected /backend-api/ps/mcp to return 403:\n%s", out)
		}
		if !strings.Contains(out, "DUMP_OK=1") {
			t.Fatalf("sandbox dump failed:\n%s", out)
		}

		mu.Lock()
		gotRefresh, gotResp := sawRefresh, sawResp
		mu.Unlock()
		if !gotRefresh || refreshCount.Load() != 1 || !gotResp {
			t.Fatalf("upstream flags: refresh=%v count=%d resp=%v", gotRefresh, refreshCount.Load(), gotResp)
		}

		// Verify host ~/.codex/auth.json was atomically updated with rotated tokens (mode 0600).
		diskToks, err := egress.ReadCodexAuthFile(hostAuthPath)
		if err != nil {
			t.Fatalf("ReadCodexAuthFile(host) after refresh: %v", err)
		}
		if string(diskToks.AccessToken) != rotAccess || string(diskToks.RefreshToken) != rotRefresh || diskToks.AccountID != rotAcct {
			t.Fatalf("host auth.json not rotated: acct=%q matchAcc=%v matchRef=%v",
				diskToks.AccountID, string(diskToks.AccessToken) == rotAccess, string(diskToks.RefreshToken) == rotRefresh)
		}
		if fi, err := os.Stat(hostAuthPath); err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("host auth.json mode=%v err=%v, want 0600", fi.Mode().Perm(), err)
		}

		// Verify sandbox dump has placeholder ONLY and ZERO real tokens or account IDs.
		dumpBytes, err := os.ReadFile(filepath.Join(wsHost, "sandbox-dump.bin"))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(dumpBytes, []byte("acct_placeholder")) {
			t.Fatal("sandbox dump missing placeholder auth.json")
		}
		for _, secret := range []string{initAccess, rotAccess, initRefresh, rotRefresh, rotID, initAcct, rotAcct} {
			if bytes.Contains(dumpBytes, []byte(secret)) {
				t.Fatalf("real ChatGPT credential leaked into sandbox!")
			}
		}

		// Verify token usage counted on /backend-api/codex/responses.
		uCtx, uCancel := context.WithTimeout(context.Background(), 5*time.Second)
		usage, err := egClient.Usage(uCtx)
		uCancel()
		if err != nil {
			t.Fatalf("Usage: %v", err)
		}
		if usage.Total.TotalTokens < 25 {
			t.Fatalf("usage = %+v, want >= 25 tokens", usage)
		}
		t.Logf("real_codex_chatgpt usage: %+v", usage.Total)

		for _, secret := range []string{initAccess, rotAccess, initRefresh, rotRefresh, rotID} {
			assertNoSecretOnDisk(t, secret, paths.SessionDir(id), paths.RuntimeDir())
		}

		_, _ = host.HolderRequest(id, "kill", nil, 3*time.Second)
		<-supDone
	})
}
