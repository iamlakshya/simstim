package guest

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
)

func TestGatewayEnvAndConfigForClaudeAndCodex(t *testing.T) {
	claude, ok := agents.Find("claude")
	if !ok {
		t.Fatal("claude row missing")
	}
	claudeEnv := GatewayEnv("http://127.0.0.1:3128", claude, "ANTHROPIC_API_KEY")
	if got := claudeEnv["ANTHROPIC_BASE_URL"]; got != "http://127.0.0.1:3128/gw/anthropic" {
		t.Errorf("ANTHROPIC_BASE_URL = %q, want http://127.0.0.1:3128/gw/anthropic", got)
	}
	if ph := claudeEnv["ANTHROPIC_API_KEY"]; ph == "" || !strings.Contains(ph, "placeholder") {
		t.Errorf("ANTHROPIC_API_KEY placeholder = %q", ph)
	}
	if path, err := WriteGatewayConfig(t.TempDir(), claudeEnv, "http://127.0.0.1:3128", claude, "ANTHROPIC_API_KEY"); err != nil || path != "" {
		t.Errorf("WriteGatewayConfig(claude) = (%q, %v), want empty path and nil err", path, err)
	}

	codex, ok := agents.Find("codex")
	if !ok {
		t.Fatal("codex row missing")
	}
	home := t.TempDir()
	codexEnv := GatewayEnv("http://127.0.0.1:3128", codex, "OPENAI_API_KEY")
	if got := codexEnv["OPENAI_BASE_URL"]; got != "http://127.0.0.1:3128/gw/openai/v1" {
		t.Errorf("OPENAI_BASE_URL = %q, want http://127.0.0.1:3128/gw/openai/v1", got)
	}
	cfgPath, err := WriteGatewayConfig(home, codexEnv, "http://127.0.0.1:3128", codex, "OPENAI_API_KEY")
	if err != nil {
		t.Fatalf("WriteGatewayConfig(codex): %v", err)
	}
	if cfgPath != filepath.Join(home, ".codex", "config.toml") {
		t.Errorf("cfgPath = %q", cfgPath)
	}
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read codex config: %v", err)
	}
	var parsed struct {
		ModelProvider  string `toml:"model_provider"`
		ModelProviders map[string]struct {
			Name               string `toml:"name"`
			BaseURL            string `toml:"base_url"`
			EnvKey             string `toml:"env_key"`
			WireAPI            string `toml:"wire_api"`
			SupportsWebSockets bool   `toml:"supports_websockets"`
		} `toml:"model_providers"`
	}
	if _, err := toml.Decode(string(raw), &parsed); err != nil {
		t.Fatalf("decode codex config.toml: %v\n%s", err, raw)
	}
	prov := parsed.ModelProviders[parsed.ModelProvider]
	if parsed.ModelProvider != "gateway" || prov.BaseURL != "http://127.0.0.1:3128/gw/openai/v1" ||
		prov.EnvKey != "OPENAI_API_KEY" || prov.WireAPI != "responses" || prov.SupportsWebSockets {
		t.Fatalf("parsed codex config = %+v", parsed)
	}

	// CODEX_HOME override places config.toml inside CODEX_HOME.
	customHome := filepath.Join(t.TempDir(), "custom-codex")
	codexEnv["CODEX_HOME"] = customHome
	overridePath, err := WriteGatewayConfig(home, codexEnv, "http://127.0.0.1:3128", codex, "CODEX_API_KEY")
	if err != nil {
		t.Fatalf("WriteGatewayConfig(CODEX_HOME): %v", err)
	}
	if overridePath != filepath.Join(customHome, "config.toml") {
		t.Errorf("overridePath = %q, want %q", overridePath, filepath.Join(customHome, "config.toml"))
	}
}

type gwTestResolver map[string][]netip.Addr

func (r gwTestResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	return r[host], nil
}

func TestGuestProxyToModelGatewayEndToEnd(t *testing.T) {
	const realKey = "sk-ant-real-host-only-secret-999"
	var seenAPIKey, seenAuth string

	// Local verified-TLS mock upstream pretending to be api.anthropic.com.
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "api.anthropic.com"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"api.anthropic.com"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	pool := x509.NewCertPool()
	parsedCert, _ := x509.ParseCertificate(der)
	pool.AddCert(parsedCert)

	upSrv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAPIKey = r.Header.Get("X-Api-Key")
		seenAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_1","model":"claude-sonnet-4-6","usage":{"input_tokens":15,"output_tokens":5}}`)
	}))
	upSrv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	upSrv.StartTLS()
	defer upSrv.Close()

	ap, err := egress.NewAddressPolicyWithHosts(nil)
	if err != nil {
		t.Fatal(err)
	}
	sd := &egress.SafeDialer{
		Addrs:    ap,
		Resolver: gwTestResolver{"api.anthropic.com": {netip.AddrFrom4([4]byte{93, 184, 216, 34})}},
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, upSrv.Listener.Addr().String())
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	supConn, egConn := net.Pipe()
	egDone := make(chan error, 1)
	go func() {
		egDone <- egress.RunOnConn(ctx, "guest-gw", egConn, &egress.ServerOptions{
			Addrs:      ap,
			Dialer:     sd,
			GatewayTLS: &tls.Config{RootCAs: pool},
		})
	}()

	cli, err := egress.NewClient(ctx, supConn, egress.StartArgs{
		Session:    "host/s1",
		Agent:      "claude",
		PolicyTOML: "[sandbox]\npreset = \"locked\"\n",
		BoundSecrets: []egress.BoundCredential{{
			Route:  "anthropic",
			Name:   "ANTHROPIC_API_KEY",
			Header: "x-api-key",
			Format: "{value}",
			Value:  api.Secret(realKey),
		}},
	}, nil)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer func() {
		cancel()
		_ = cli.Close()
		<-egDone
	}()

	gp, err := ListenProxy(ctx, cli, "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenProxy: %v", err)
	}
	go gp.Serve(ctx)
	defer func() { _ = gp.Close() }()

	claude, _ := agents.Find("claude")
	proxyURL := "http://" + gp.Addr().String()
	gwEnv := GatewayEnv(proxyURL, claude, "ANTHROPIC_API_KEY")
	for k, v := range gwEnv {
		if strings.Contains(v, realKey) {
			t.Fatalf("real key leaked into guest env %s", k)
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, gwEnv["ANTHROPIC_BASE_URL"]+"/v1/messages",
		strings.NewReader(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", gwEnv["ANTHROPIC_API_KEY"])
	req.Header.Set("Authorization", "Bearer planted-attacker-token")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST gateway: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body = %s", resp.StatusCode, body)
	}
	if seenAPIKey != realKey || seenAuth != "" {
		t.Fatalf("upstream headers: x-api-key=%q Authorization=%q, want real key only", seenAPIKey, seenAuth)
	}
	usage, err := cli.Usage(ctx)
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if usage.Total.TotalTokens != 20 || usage.Total.InputTokens != 15 || usage.Total.OutputTokens != 5 {
		t.Errorf("usage = %+v, want total=20 input=15 output=5", usage)
	}
}

func TestSubscriptionGatewayConfigAndPlaceholderAuthJSON(t *testing.T) {
	codex, ok := agents.Find("codex")
	if !ok || codex.SubscriptionGateway == nil {
		t.Fatal("codex SubscriptionGateway missing")
	}
	home := t.TempDir()
	envMap := map[string]string{"HOME": home}
	cfgPath, authPath, err := WriteSubscriptionGatewayConfig(home, envMap, "http://127.0.0.1:3128", codex)
	if err != nil {
		t.Fatalf("WriteSubscriptionGatewayConfig: %v", err)
	}
	if cfgPath != filepath.Join(home, ".codex", "config.toml") || authPath != filepath.Join(home, ".codex", "auth.json") {
		t.Fatalf("paths = (%q, %q)", cfgPath, authPath)
	}
	rawCfg, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config.toml: %v", err)
	}
	var parsed struct {
		CredStore      string `toml:"cli_auth_credentials_store"`
		ModelProvider  string `toml:"model_provider"`
		ChatGPTBaseURL string `toml:"chatgpt_base_url"`
		ModelProviders map[string]struct {
			BaseURL            string `toml:"base_url"`
			WireAPI            string `toml:"wire_api"`
			RequiresOpenAIAuth bool   `toml:"requires_openai_auth"`
			SupportsWebSockets bool   `toml:"supports_websockets"`
		} `toml:"model_providers"`
	}
	if _, err := toml.Decode(string(rawCfg), &parsed); err != nil {
		t.Fatalf("decode config.toml: %v\n%s", err, rawCfg)
	}
	prov := parsed.ModelProviders[parsed.ModelProvider]
	if parsed.CredStore != "file" || parsed.ModelProvider != "gateway" ||
		parsed.ChatGPTBaseURL != "http://127.0.0.1:3128/gw/chatgpt/backend-api" ||
		prov.BaseURL != "http://127.0.0.1:3128/gw/chatgpt/backend-api/codex" ||
		prov.WireAPI != "responses" || !prov.RequiresOpenAIAuth || prov.SupportsWebSockets {
		t.Fatalf("unexpected subscription config.toml: %+v", parsed)
	}

	fi, err := os.Stat(authPath)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("auth.json stat: err=%v mode=%v", err, fi.Mode().Perm())
	}
	toks, err := egress.ReadCodexAuthFile(authPath)
	if err != nil {
		t.Fatalf("ReadCodexAuthFile(placeholder): %v", err)
	}
	if toks.AccountID != "acct_placeholder" {
		t.Errorf("placeholder account_id = %q, want acct_placeholder", toks.AccountID)
	}
	if toks.ExpiresAt < time.Now().Add(10*365*24*time.Hour).Unix() {
		t.Errorf("placeholder exp = %d, want far-future expiry", toks.ExpiresAt)
	}

	// applyStartGateway in subscription mode writes subscription config and does
	// NOT export OPENAI_API_KEY / OPENAI_BASE_URL, while keeping other bound placeholders.
	home2 := t.TempDir()
	env2, err := applyStartGateway(StartReq{
		Agent:             "codex",
		ProxyURL:          "http://127.0.0.1:3128",
		SubscriptionRoute: "chatgpt",
		BoundPlaceholders: []string{"ACME_TOKEN=placeholder-acme"},
	}, []string{"HOME=" + home2})
	if err != nil {
		t.Fatalf("applyStartGateway(subscription): %v", err)
	}
	envMap2 := map[string]string{}
	for _, kv := range env2 {
		if k, v, ok := strings.Cut(kv, "="); ok {
			envMap2[k] = v
		}
	}
	if _, hasKey := envMap2["OPENAI_API_KEY"]; hasKey {
		t.Errorf("OPENAI_API_KEY should not be set in subscription mode: %v", envMap2)
	}
	if _, hasURL := envMap2["OPENAI_BASE_URL"]; hasURL {
		t.Errorf("OPENAI_BASE_URL should not be set in subscription mode: %v", envMap2)
	}
	if envMap2["ACME_TOKEN"] != "placeholder-acme" {
		t.Errorf("ACME_TOKEN = %q, want placeholder-acme", envMap2["ACME_TOKEN"])
	}
	if _, err := os.Stat(filepath.Join(home2, ".codex", "auth.json")); err != nil {
		t.Errorf("applyStartGateway did not write placeholder auth.json: %v", err)
	}
}
