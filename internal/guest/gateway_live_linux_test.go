//go:build linux

package guest

import (
	"bufio"
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

	"golang.org/x/sys/unix"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/link"
)

const (
	liveProofEnv     = "EGRESS_LIVE_GATEWAY_PROOF"
	liveGuestModeEnv = "EGRESS_LIVE_GUEST_MODE"
)

func TestMain(m *testing.M) {
	if os.Getenv(liveGuestModeEnv) == "1" {
		if err := runLiveGuestInsideBwrap(); err != nil {
			fmt.Fprintf(os.Stderr, "live guest failed: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type guestLinkOpener struct {
	sess *link.Session
}

func (g *guestLinkOpener) OpenEgress(ctx context.Context, hdr egress.StreamHeader) (*link.Stream, error) {
	return g.sess.Open(ctx, link.KindEgress, hdr)
}

var guestEgressOnlyOpener = map[link.Kind]link.Side{
	link.KindEgress: link.Guest,
}

func runLiveGuestInsideBwrap() error {
	f := os.NewFile(3, "guest-egress-link")
	if f == nil {
		return fmt.Errorf("fd 3 missing inside bwrap")
	}
	conn, err := net.FileConn(f)
	_ = f.Close()
	if err != nil {
		return fmt.Errorf("attach fd 3: %w", err)
	}
	sess := link.New(conn, link.Guest, link.Options{Opener: guestEgressOnlyOpener})
	defer func() { _ = sess.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	gp, err := ListenProxy(ctx, &guestLinkOpener{sess: sess}, "127.0.0.1:3128")
	if err != nil {
		return fmt.Errorf("listen guest proxy: %w", err)
	}
	go gp.Serve(ctx)
	defer func() { _ = gp.Close() }()

	proxyURL := "http://" + gp.Addr().String()
	homeDir := os.Getenv("HOME")
	claudeBin := os.Getenv("LIVE_CLAUDE_BIN")
	codexBin := os.Getenv("LIVE_CODEX_BIN")
	dumpPath := filepath.Join(homeDir, "sandbox-audit-dump.txt")

	// 1. Verify direct CONNECT to both model hosts is refused from inside the sandbox.
	for _, target := range []string{"api.anthropic.com:443", "api.openai.com:443"} {
		if err := assertDirectConnectRefused(gp.Addr().String(), target); err != nil {
			return err
		}
	}

	// 2. Run real Claude Code CLI through ANTHROPIC_BASE_URL (with its real default tools).
	claudeAgent, _ := agents.Find("claude")
	claudeEnv := buildSandboxEnv(proxyURL, claudeAgent, "ANTHROPIC_API_KEY", homeDir)
	cCmd := exec.CommandContext(ctx, claudeBin, "--bare", "-p", "Say hello", "--output-format", "text", "--no-session-persistence")
	cCmd.Dir = homeDir
	cCmd.Env = claudeEnv
	cOut, cErr := cCmd.CombinedOutput()
	fmt.Printf("CLAUDE_EXIT=%v\nCLAUDE_OUT=%s\n", cErr, strings.TrimSpace(string(cOut)))
	if cErr != nil {
		return fmt.Errorf("claude CLI failed: %w\noutput: %s", cErr, cOut)
	}
	if !strings.Contains(string(cOut), "hello from mock anthropic gateway") {
		return fmt.Errorf("unexpected claude output: %q", string(cOut))
	}

	// 3. Run real Codex CLI through generated .codex/config.toml (supports_websockets = false).
	codexAgent, _ := agents.Find("codex")
	codexEnvMap := ProxyEnv(proxyURL, codexAgent)
	for k, v := range GatewayEnv(proxyURL, codexAgent, "OPENAI_API_KEY") {
		codexEnvMap[k] = v
	}
	codexHome := filepath.Join(homeDir, ".codex")
	codexEnvMap["CODEX_HOME"] = codexHome
	if _, err := WriteGatewayConfig(homeDir, codexEnvMap, proxyURL, codexAgent, "OPENAI_API_KEY"); err != nil {
		return fmt.Errorf("write codex gateway config: %w", err)
	}
	codexEnv := mergeEnv(os.Environ(), codexEnvMap)
	xCmd := exec.CommandContext(ctx, codexBin, "exec", "--strict-config", "--skip-git-repo-check", "-m", "gpt-5", "Say hello")
	xCmd.Dir = homeDir
	xCmd.Env = codexEnv
	xOut, xErr := xCmd.CombinedOutput()
	fmt.Printf("CODEX_EXIT=%v\nCODEX_OUT=%s\n", xErr, strings.TrimSpace(string(xOut)))
	if xErr != nil {
		return fmt.Errorf("codex CLI failed: %w\noutput: %s", xErr, xOut)
	}
	if !strings.Contains(string(xOut), "hello from mock openai gateway") {
		return fmt.Errorf("unexpected codex output: %q", string(xOut))
	}

	// 3b. Verify a forbidden server tool (web_fetch / mcp) is refused from inside the sandbox with 403 and x-should-retry: false.
	if err := assertForbiddenToolRefused(proxyURL+"/gw/anthropic/v1/messages", `{"tools":[{"type":"web_fetch_20260318","name":"wf"}]}`); err != nil {
		return err
	}
	if err := assertForbiddenToolRefused(proxyURL+"/gw/openai/v1/responses", `{"tools":[{"type":"mcp","server_url":"https://evil.example"}]}`); err != nil {
		return err
	}

	// 4. Snapshot sandbox env and /proc (environ + cmdline) for the trusted side to verify zero secret leakage.
	var dump bytes.Buffer
	dump.WriteString("=== CLAUDE ENV ===\n")
	for _, kv := range claudeEnv {
		dump.WriteString(kv + "\n")
	}
	dump.WriteString("=== CODEX ENV ===\n")
	for _, kv := range codexEnv {
		dump.WriteString(kv + "\n")
	}
	dump.WriteString("=== PROC ===\n")
	entries, _ := os.ReadDir("/proc")
	for _, e := range entries {
		if !e.IsDir() || len(e.Name()) == 0 || e.Name()[0] < '0' || e.Name()[0] > '9' {
			continue
		}
		for _, fn := range []string{"environ", "cmdline"} {
			if b, rerr := os.ReadFile(filepath.Join("/proc", e.Name(), fn)); rerr == nil {
				fmt.Fprintf(&dump, "/proc/%s/%s: ", e.Name(), fn)
				dump.Write(b)
				dump.WriteByte('\n')
			}
		}
	}
	return os.WriteFile(dumpPath, dump.Bytes(), 0o600)
}

func assertDirectConnectRefused(proxyAddr, target string) error {
	c, err := net.DialTimeout("tcp", proxyAddr, 3*time.Second)
	if err != nil {
		return fmt.Errorf("dial guest proxy: %w", err)
	}
	defer func() { _ = c.Close() }()
	_, _ = io.WriteString(c, "CONNECT "+target+" HTTP/1.1\r\nHost: "+target+"\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(c), &http.Request{Method: http.MethodConnect})
	if err != nil {
		return fmt.Errorf("read CONNECT %s response: %w", target, err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		return fmt.Errorf("CONNECT %s = %d %q, want 403", target, resp.StatusCode, b)
	}
	return nil
}

func assertForbiddenToolRefused(targetURL, body string) error {
	resp, err := http.Post(targetURL, "application/json", strings.NewReader(body))
	if err != nil {
		return fmt.Errorf("POST %s: %w", targetURL, err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || resp.Header.Get("x-should-retry") != "false" {
		return fmt.Errorf("POST %s = %d retry=%q body=%s, want 403/false", targetURL, resp.StatusCode, resp.Header.Get("x-should-retry"), raw)
	}
	return nil
}

func buildSandboxEnv(proxyURL string, agent agents.Agent, boundEnv, homeDir string) []string {
	m := ProxyEnv(proxyURL, agent)
	for k, v := range GatewayEnv(proxyURL, agent, boundEnv) {
		m[k] = v
	}
	m["HOME"] = homeDir
	return mergeEnv(os.Environ(), m)
}

func mergeEnv(base []string, overrides map[string]string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(base)+len(overrides))
	for _, kv := range base {
		k, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if v, repl := overrides[k]; repl {
			out = append(out, k+"="+v)
			seen[k] = true
			continue
		}
		out = append(out, kv)
	}
	for k, v := range overrides {
		if !seen[k] {
			out = append(out, k+"="+v)
		}
	}
	return out
}

func randomLiveSecret(prefix string) string {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	return prefix + hex.EncodeToString(buf)
}

// TestLiveAgentsThroughBwrapGateway proves P2.4 on a real Linux host (host-a)
// using bubblewrap (--unshare-net --unshare-pid), the real Claude Code CLI and
// real Codex CLI against a local verified-TLS mock upstream. Opt-in via
// EGRESS_LIVE_GATEWAY_PROOF=1.
func TestLiveAgentsThroughBwrapGateway(t *testing.T) {
	if os.Getenv(liveProofEnv) != "1" {
		t.Skipf("set %s=1 to run the live bwrap + claude/codex proof", liveProofEnv)
	}
	bwrapBin, err := exec.LookPath("bwrap")
	if err != nil {
		t.Fatalf("bwrap not found: %v", err)
	}
	claudeBin, err := exec.LookPath("claude")
	if err != nil {
		t.Fatalf("claude not found: %v", err)
	}
	codexBin, err := exec.LookPath("codex")
	if err != nil {
		t.Fatalf("codex not found: %v", err)
	}

	// Generate random host-only tokens at runtime; never pass them into bwrap.
	anthroKey := randomLiveSecret("sk-ant-api03-live-")
	openAIKey := randomLiveSecret("sk-proj-live-")

	var (
		mu              sync.Mutex
		sawAnthropicKey bool
		sawOpenAIKey    bool
		sawWSUpgrade    bool
	)
	upSrv, pool := startLiveMockUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		t.Logf("UPSTREAM %s https://%s%s x-api-key-match=%v auth-match=%v upgrade=%q",
			r.Method, r.Host, r.URL.Path,
			r.Header.Get("X-Api-Key") == anthroKey,
			r.Header.Get("Authorization") == "Bearer "+openAIKey,
			r.Header.Get("Upgrade"))

		mu.Lock()
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			sawWSUpgrade = true
		}
		if r.Host == "api.anthropic.com" && r.Header.Get("X-Api-Key") == anthroKey {
			sawAnthropicKey = true
		}
		if r.Host == "api.openai.com" && r.Header.Get("Authorization") == "Bearer "+openAIKey {
			sawOpenAIKey = true
		}
		mu.Unlock()

		writeLiveMockResponse(w, r, body)
	})

	ap, err := egress.NewAddressPolicyWithHosts(nil)
	if err != nil {
		t.Fatal(err)
	}
	sd := &egress.SafeDialer{
		Addrs: ap,
		Resolver: gwTestResolver{
			"api.anthropic.com": {netip.AddrFrom4([4]byte{93, 184, 216, 34})},
			"api.openai.com":    {netip.AddrFrom4([4]byte{93, 184, 216, 34})},
		},
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, upSrv.Listener.Addr().String())
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	supEg, egConn := net.Pipe()
	egDone := make(chan error, 1)
	go func() {
		egDone <- egress.RunOnConn(ctx, "host-a-live", egConn, &egress.ServerOptions{
			Addrs:      ap,
			Dialer:     sd,
			GatewayTLS: &tls.Config{RootCAs: pool},
		})
	}()

	cli, err := egress.NewClient(ctx, supEg, egress.StartArgs{
		Session:    "host-a/live-proof",
		PolicyTOML: "[sandbox]\npreset = \"balanced\"\n",
		BoundSecrets: []egress.BoundCredential{
			{Route: "anthropic", Name: "ANTHROPIC_API_KEY", Header: "x-api-key", Format: "{value}", Value: api.Secret(anthroKey)},
			{Route: "openai", Name: "OPENAI_API_KEY", Header: "Authorization", Format: "Bearer {value}", Value: api.Secret(openAIKey)},
		},
	}, func(ev egress.AuditEvent) {
		t.Logf("AUDIT mode=%s host=%s %s %s status=%d decision=%s secret=%s why=%q usage=%s",
			ev.Mode, ev.Host, ev.Method, ev.Path, ev.Status, ev.Decision, ev.Secret, ev.Why, ev.UsageKind)
	})
	if err != nil {
		t.Fatalf("egress.NewClient: %v", err)
	}
	defer func() {
		cancel()
		_ = cli.Close()
		<-egDone
	}()

	// Create a Unix socketpair between supervisor relay and bwrap guest (fd 3).
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatalf("Socketpair: %v", err)
	}
	supFile := os.NewFile(uintptr(fds[0]), "sup-guest-link")
	guestFile := os.NewFile(uintptr(fds[1]), "guest-link")
	defer func() { _ = supFile.Close() }()
	supGuestConn, err := net.FileConn(supFile)
	if err != nil {
		_ = guestFile.Close()
		t.Fatalf("FileConn: %v", err)
	}
	defer func() { _ = supGuestConn.Close() }()

	guestSess := link.New(supGuestConn, link.Supervisor, link.Options{Opener: guestEgressOnlyOpener})
	defer func() { _ = guestSess.Close() }()

	// Supervisor relay: forwards ONLY KindEgress streams from guest to egress.
	go func() {
		for {
			gs, aerr := guestSess.Accept(ctx)
			if aerr != nil {
				return
			}
			go relayGuestStream(ctx, gs, cli)
		}
	}()

	sandboxHome := t.TempDir()
	selfBin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, bwrapBin,
		"--unshare-net",
		"--unshare-pid",
		"--ro-bind", "/", "/",
		"--dev", "/dev",
		"--proc", "/proc",
		"--tmpfs", "/tmp",
		"--bind", sandboxHome, "/tmp/sandbox-home",
		"--ro-bind", selfBin, "/tmp/live.test",
		"--setenv", "HOME", "/tmp/sandbox-home",
		"--setenv", liveGuestModeEnv, "1",
		"--setenv", "LIVE_CLAUDE_BIN", claudeBin,
		"--setenv", "LIVE_CODEX_BIN", codexBin,
		"--die-with-parent",
		"/tmp/live.test",
	)
	cmd.ExtraFiles = []*os.File{guestFile}
	out, runErr := cmd.CombinedOutput()
	_ = guestFile.Close()
	t.Logf("BWRAP OUTPUT:\n%s", string(out))
	if runErr != nil {
		t.Fatalf("bwrap live guest failed: %v", runErr)
	}

	// Verify on the trusted side that neither random secret appeared anywhere in
	// sandbox output, env, /proc dump or any file written inside sandboxHome.
	if strings.Contains(string(out), anthroKey) || strings.Contains(string(out), openAIKey) {
		t.Fatal("host secret appeared in sandbox output")
	}
	fileCount := 0
	walkErr := filepath.WalkDir(sandboxHome, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		fileCount++
		b, rerr := os.ReadFile(p)
		if rerr == nil && (strings.Contains(string(b), anthroKey) || strings.Contains(string(b), openAIKey)) {
			return fmt.Errorf("host secret leaked into sandbox file %s", p)
		}
		return nil
	})
	if walkErr != nil {
		t.Fatal(walkErr)
	}
	t.Logf("LEAK CHECK: scanned %d files (including env + /proc dump) under sandboxHome; 0 secret matches", fileCount)

	mu.Lock()
	defer mu.Unlock()
	if !sawAnthropicKey {
		t.Error("mock upstream never saw real Anthropic key from Claude Code")
	}
	if !sawOpenAIKey {
		t.Error("mock upstream never saw real OpenAI key from Codex")
	}
	if sawWSUpgrade {
		t.Error("mock upstream received a WebSocket upgrade despite supports_websockets = false")
	}
	usage, err := cli.Usage(ctx)
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	t.Logf("SESSION USAGE: %+v", usage)
	if usage.Total.TotalTokens == 0 || usage.Total.Requests < 2 {
		t.Errorf("expected usage from both Claude and Codex, got %+v", usage)
	}
}

func relayGuestStream(ctx context.Context, gs *link.Stream, cli *egress.Client) {
	defer func() { _ = gs.Close() }()
	if gs.Kind() != link.KindEgress {
		return
	}
	var hdr egress.StreamHeader
	if err := gs.Header(&hdr); err != nil {
		return
	}
	es, err := cli.OpenEgress(ctx, hdr)
	if err != nil {
		return
	}
	defer func() { _ = es.Close() }()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(es, gs)
		_ = es.CloseWrite()
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(gs, es)
		_ = gs.CloseWrite()
	}()
	wg.Wait()
}

func startLiveMockUpstream(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(9),
		Subject:      pkix.Name{CommonName: "api.anthropic.com"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"api.anthropic.com", "api.openai.com"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	pool := x509.NewCertPool()
	parsed, _ := x509.ParseCertificate(der)
	pool.AddCert(parsed)
	srv := httptest.NewUnstartedServer(handler)
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv, pool
}

func writeLiveMockResponse(w http.ResponseWriter, r *http.Request, reqBody []byte) {
	switch {
	case r.Host == "api.anthropic.com" && r.URL.Path == "/v1/messages/count_tokens":
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"input_tokens":12}`)
	case r.Host == "api.anthropic.com" && r.URL.Path == "/v1/messages":
		var req struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		_ = json.Unmarshal(reqBody, &req)
		if req.Model == "" {
			req.Model = "claude-sonnet-4-6"
		}
		if req.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintf(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_live\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":%q,\"stop_reason\":null,\"stop_sequence\":null,\"usage\":{\"input_tokens\":42,\"cache_creation_input_tokens\":0,\"cache_read_input_tokens\":10,\"output_tokens\":1}}}\n\n", req.Model)
			_, _ = io.WriteString(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
			_, _ = io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello from mock anthropic gateway\"}}\n\n")
			_, _ = io.WriteString(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
			_, _ = io.WriteString(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":7}}\n\n")
			_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"msg_live","type":"message","role":"assistant","model":%q,"content":[{"type":"text","text":"hello from mock anthropic gateway"}],"stop_reason":"end_turn","usage":{"input_tokens":42,"output_tokens":7}}`, req.Model)
	case r.Host == "api.openai.com" && r.URL.Path == "/v1/models":
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"gpt-5","object":"model"}]}`)
	case r.Host == "api.openai.com" && r.URL.Path == "/v1/responses":
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_live\",\"object\":\"response\",\"status\":\"in_progress\",\"model\":\"gpt-5\",\"output\":[]}}\n\n")
		_, _ = io.WriteString(w, "event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"msg_live\",\"type\":\"message\",\"status\":\"in_progress\",\"role\":\"assistant\",\"content\":[]}}\n\n")
		_, _ = io.WriteString(w, "event: response.content_part.added\ndata: {\"type\":\"response.content_part.added\",\"item_id\":\"msg_live\",\"output_index\":0,\"content_index\":0,\"part\":{\"type\":\"output_text\",\"text\":\"\",\"annotations\":[]}}\n\n")
		_, _ = io.WriteString(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_live\",\"output_index\":0,\"content_index\":0,\"delta\":\"hello from mock openai gateway\"}\n\n")
		_, _ = io.WriteString(w, "event: response.output_text.done\ndata: {\"type\":\"response.output_text.done\",\"item_id\":\"msg_live\",\"output_index\":0,\"content_index\":0,\"text\":\"hello from mock openai gateway\"}\n\n")
		_, _ = io.WriteString(w, "event: response.content_part.done\ndata: {\"type\":\"response.content_part.done\",\"item_id\":\"msg_live\",\"output_index\":0,\"content_index\":0,\"part\":{\"type\":\"output_text\",\"text\":\"hello from mock openai gateway\",\"annotations\":[]}}\n\n")
		_, _ = io.WriteString(w, "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"msg_live\",\"type\":\"message\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello from mock openai gateway\",\"annotations\":[]}]}}\n\n")
		_, _ = io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_live\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"gpt-5\",\"output\":[{\"id\":\"msg_live\",\"type\":\"message\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello from mock openai gateway\",\"annotations\":[]}]}],\"usage\":{\"input_tokens\":55,\"input_tokens_details\":{\"cached_tokens\":15},\"output_tokens\":9,\"output_tokens_details\":{\"reasoning_tokens\":0},\"total_tokens\":64}}}\n\n")
	default:
		http.NotFound(w, r)
	}
}
