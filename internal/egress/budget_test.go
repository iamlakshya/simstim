package egress

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
)

func TestModelBudgetAnthropicAndOpenAIRefusalAndLiveReload(t *testing.T) {
	var upstreamHits atomic.Int32
	inFlightEntered := make(chan struct{})
	releaseInFlight := make(chan struct{})

	upSrv, pool := startMockModelUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		upstreamHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/messages/count_tokens":
			_, _ = io.WriteString(w, `{"input_tokens":500}`)
		case "/v1/messages":
			if r.URL.Query().Get("slow") == "1" {
				close(inFlightEntered)
				<-releaseInFlight
				_, _ = io.WriteString(w, `{"id":"msg_slow","model":"claude-sonnet-4-6","usage":{"input_tokens":80,"output_tokens":40}}`)
				return
			}
			_, _ = io.WriteString(w, `{"id":"msg_1","model":"claude-sonnet-4-6","usage":{"input_tokens":150,"output_tokens":60}}`)
		case "/v1/responses":
			_, _ = io.WriteString(w, `{"id":"resp_1","model":"gpt-5","usage":{"input_tokens":50,"output_tokens":20,"total_tokens":70}}`)
		case "/v1/chat/completions":
			_, _ = io.WriteString(w, `{"id":"chat_1","model":"gpt-4.1","usage":{"prompt_tokens":20,"completion_tokens":10,"total_tokens":30}}`)
		}
	}, "api.anthropic.com", "api.openai.com")

	cli, rec, hc := startGatewayTestEgress(t, StartArgs{
		Session: "devbox/auth-fix",
		PolicyTOML: `
[sandbox]
preset = "balanced"

[network]
model_budget = 250
`,
		BoundSecrets: []BoundCredential{
			{Route: "anthropic", Name: "ANTHROPIC_API_KEY", Header: "x-api-key", Value: api.Secret("k1")},
			{Route: "openai", Name: "OPENAI_API_KEY", Header: "Authorization", Format: "Bearer {value}", Value: api.Secret("k2")},
		},
	}, upSrv.Listener.Addr().String(), pool)

	// Start one slow request while usage is 0 (under budget=250).
	slowDone := make(chan int, 1)
	go func() {
		resp, err := hc.Post("http://127.0.0.1:3128/gw/anthropic/v1/messages?slow=1", "application/json", strings.NewReader(`{"model":"claude-sonnet-4-6","messages":[]}`))
		if err != nil {
			slowDone <- -1
			return
		}
		_, _ = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		slowDone <- resp.StatusCode
	}()
	<-inFlightEntered

	// While slow request is in flight, a second request crosses 210 tokens (still < 250).
	resp, err := hc.Post("http://127.0.0.1:3128/gw/anthropic/v1/messages", "application/json", strings.NewReader(`{"model":"claude-sonnet-4-6","messages":[]}`))
	if err != nil {
		t.Fatalf("first anthropic request: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first anthropic status = %d, want 200", resp.StatusCode)
	}

	// Release the in-flight request: spec 17.8 says a request in flight finishes
	// even when its answer pushes cumulative usage past the budget (210 + 120 = 330 >= 250).
	close(releaseInFlight)
	if code := <-slowDone; code != http.StatusOK {
		t.Fatalf("in-flight anthropic status = %d, want 200", code)
	}

	summary, err := cli.Usage(context.Background())
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if summary.Total.TotalTokens != 330 || summary.Budget != 250 || summary.Total.Requests != 2 {
		t.Fatalf("Usage = %+v, want total=330 budget=250 requests=2", summary)
	}

	hitsBeforeRefusal := upstreamHits.Load()

	// Next Anthropic request must get HTTP 429 billing_error, x-should-retry: false, no Retry-After.
	resp, err = hc.Post("http://127.0.0.1:3128/gw/anthropic/v1/messages", "application/json", strings.NewReader(`{"model":"claude-sonnet-4-6","messages":[]}`))
	if err != nil {
		t.Fatalf("over-budget anthropic request: %v", err)
	}
	anthBody, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("over-budget anthropic status = %d, want 429 (body=%s)", resp.StatusCode, anthBody)
	}
	if resp.Header.Get("x-should-retry") != "false" {
		t.Errorf("anthropic x-should-retry = %q, want false", resp.Header.Get("x-should-retry"))
	}
	if resp.Header.Get("Retry-After") != "" {
		t.Errorf("anthropic unexpectedly set Retry-After = %q", resp.Header.Get("Retry-After"))
	}
	var anthErr struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(anthBody, &anthErr); err != nil {
		t.Fatalf("decode anthropic 429 body: %v (%s)", err, anthBody)
	}
	wantLine := fmt.Sprintf("%s: devbox/auth-fix used 330 of its 250 model tokens. Try: raise network.model_budget with %s policy edit, then %s policy apply",
		brand.Name, brand.Name, brand.Name)
	if anthErr.Type != "error" || anthErr.Error.Type != "billing_error" || anthErr.Error.Message != wantLine || !strings.HasPrefix(anthErr.RequestID, "req_") {
		t.Errorf("anthropic 429 payload = %+v, want billing_error with %q", anthErr, wantLine)
	}

	// Next OpenAI Responses and Chat requests must get HTTP 429 insufficient_quota (both type and code), x-should-retry: false, no Retry-After.
	for _, ep := range []string{"/gw/openai/v1/responses", "/gw/openai/v1/chat/completions"} {
		resp, err = hc.Post("http://127.0.0.1:3128"+ep, "application/json", strings.NewReader(`{"model":"gpt-5","input":"hi"}`))
		if err != nil {
			t.Fatalf("over-budget openai %s: %v", ep, err)
		}
		oaiBody, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("over-budget openai %s status = %d, want 429 (body=%s)", ep, resp.StatusCode, oaiBody)
		}
		if resp.Header.Get("x-should-retry") != "false" {
			t.Errorf("openai %s x-should-retry = %q, want false", ep, resp.Header.Get("x-should-retry"))
		}
		if resp.Header.Get("Retry-After") != "" {
			t.Errorf("openai %s unexpectedly set Retry-After = %q", ep, resp.Header.Get("Retry-After"))
		}
		var oaiErr struct {
			Error struct {
				Message string `json:"message"`
				Type    string `json:"type"`
				Param   any    `json:"param"`
				Code    string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(oaiBody, &oaiErr); err != nil {
			t.Fatalf("decode openai 429 body: %v (%s)", err, oaiBody)
		}
		if oaiErr.Error.Type != "insufficient_quota" || oaiErr.Error.Code != "insufficient_quota" || oaiErr.Error.Param != nil || oaiErr.Error.Message != wantLine {
			t.Errorf("openai %s 429 payload = %+v, want insufficient_quota with %q", ep, oaiErr, wantLine)
		}
	}

	// None of the three refused requests reached upstream.
	if got := upstreamHits.Load(); got != hitsBeforeRefusal {
		t.Errorf("upstream hits after budget refusals = %d, want %d", got, hitsBeforeRefusal)
	}

	// Verify audit event recorded model_budget refusal.
	budgetEv := rec.waitFor(t, func(e AuditEvent) bool {
		return e.Rule == "model_budget" && e.Status == http.StatusTooManyRequests && e.Decision == string(ActionDeny)
	})
	if budgetEv.BudgetUsed != 330 || budgetEv.BudgetLimit != 250 {
		t.Errorf("budget audit event = %+v, want used=330 limit=250", budgetEv)
	}

	// /v1/messages/count_tokens is free and stays allowed even over budget.
	resp, err = hc.Post("http://127.0.0.1:3128/gw/anthropic/v1/messages/count_tokens", "application/json", strings.NewReader(`{"messages":[]}`))
	if err != nil {
		t.Fatalf("count_tokens: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("count_tokens status = %d, want 200", resp.StatusCode)
	}

	// Raising network.model_budget live lets the next request through immediately (spec 17.8).
	if err := cli.Reload(context.Background(), "session = \"devbox/auth-fix\"\n[sandbox]\npreset = \"balanced\"\n[network]\nmodel_budget = 1000\n"); err != nil {
		t.Fatalf("Reload higher budget: %v", err)
	}
	resp, err = hc.Post("http://127.0.0.1:3128/gw/openai/v1/responses", "application/json", strings.NewReader(`{"model":"gpt-5","input":"after raise"}`))
	if err != nil {
		t.Fatalf("openai after raise: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("openai after budget raise status = %d, want 200", resp.StatusCode)
	}
	summary, err = cli.Usage(context.Background())
	if err != nil {
		t.Fatalf("Usage after raise: %v", err)
	}
	if summary.Total.TotalTokens != 400 || summary.Budget != 1000 {
		t.Errorf("Usage after raise = %+v, want total=400 budget=1000", summary)
	}
}

func TestUsageCutAnthropicAfterMessageStartAndSeededBudget(t *testing.T) {
	// Cut Anthropic stream AFTER message_start (with output_tokens=1) and
	// content_block_delta, BEFORE message_delta/message_stop -> must fall back
	// to estimate rather than reporting partial output_tokens=1.
	const cutAfterStart = "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"m1","model":"claude-sonnet-4-6","usage":{"input_tokens":50,"output_tokens":1}}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","delta":{"text":"partial output before disconnect"}}` + "\n\n"

	obs := newUsageObserver(WireAnthropicMessages, 40, "text/event-stream")
	obs.Write([]byte(cutAfterStart))
	got := obs.Finish()
	wantEst := int64(40+len(cutAfterStart)+3) / 4
	if !got.Estimated || got.TotalTokens != wantEst || got.Model != "claude-sonnet-4-6" {
		t.Errorf("cut Anthropic SSE = %+v, want Estimated=true TotalTokens=%d Model=claude-sonnet-4-6", got, wantEst)
	}

	// OpenAI Responses response.incomplete with cache_write_tokens -> exact usage.
	const incompleteSSE = "event: response.incomplete\n" +
		`data: {"type":"response.incomplete","response":{"model":"gpt-5","usage":{"input_tokens":120,"cache_write_tokens":30,"input_tokens_details":{"cached_tokens":20},"output_tokens":45,"output_tokens_details":{"reasoning_tokens":15},"total_tokens":165}}}` + "\n\n"
	obs2 := newUsageObserver(WireOpenAIResponses, 20, "text/event-stream")
	obs2.Write([]byte(incompleteSSE))
	got2 := obs2.Finish()
	if got2.Estimated || got2.TotalTokens != 165 || got2.CacheCreationTokens != 30 || got2.CacheReadTokens != 20 || got2.ReasoningTokens != 15 {
		t.Errorf("OpenAI response.incomplete = %+v, want exact total=165 cacheWrite=30 cached=20 reasoning=15", got2)
	}

	// Seeded InitialUsage preserves prior token counts and enforces budget on restart.
	upSrv, pool := startMockModelUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("upstream must not be called when seeded usage already reached model_budget")
		w.WriteHeader(http.StatusOK)
	}, "api.anthropic.com")
	_, _, hc := startGatewayTestEgress(t, StartArgs{
		Session:    "devbox/restored",
		PolicyTOML: "[network]\nmodel_budget = 500\n",
		BoundSecrets: []BoundCredential{
			{Route: "anthropic", Name: "ANTHROPIC_API_KEY", Header: "x-api-key", Value: api.Secret("k1")},
		},
		InitialUsage: &UsageSummary{
			Total:   TokenUsage{TotalTokens: 520, Requests: 3},
			ByModel: map[string]TokenUsage{"claude-sonnet-4-6": {Model: "claude-sonnet-4-6", TotalTokens: 520, Requests: 3}},
		},
	}, upSrv.Listener.Addr().String(), pool)

	resp, err := hc.Post("http://127.0.0.1:3128/gw/anthropic/v1/messages", "application/json", strings.NewReader(`{"model":"claude-sonnet-4-6","messages":[]}`))
	if err != nil {
		t.Fatalf("seeded over-budget request: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("seeded over-budget status = %d, want 429", resp.StatusCode)
	}
}

func TestStricterWildcardPublicAndPrivateSuffixes(t *testing.T) {
	// P2.7 acceptance: *.vercel.app is refused, and app.vercel.app passes.
	refused := []string{
		"*.vercel.app",
		"*.github.io",
		"*.herokuapp.com",
		"*.s3.amazonaws.com",
		"*.pages.dev",
		"*.netlify.app",
		"*.co.uk",
		"*.com",
	}
	for _, pat := range refused {
		if _, err := NewEngine(Config{Network: NetworkConfig{Allow: []string{pat}}}); err == nil || !strings.Contains(err.Error(), "Try:") {
			t.Errorf("Allow %q = %v, want error with Try: hint", pat, err)
		}
		eng, err := NewEngine(Config{})
		if err != nil {
			t.Fatal(err)
		}
		if err := eng.Allow(pat); err == nil || !strings.Contains(err.Error(), "Try:") {
			t.Errorf("live Allow(%q) = %v, want error with Try: hint", pat, err)
		}
		// Denying a broad shared suffix stays allowed (narrowing is always safe).
		if _, err := NewEngine(Config{Network: NetworkConfig{Deny: []string{pat}}}); err != nil {
			t.Errorf("Deny %q = %v, want nil", pat, err)
		}
	}

	// Specific hosts and subdomains of specific hosts on shared suffixes pass.
	eng, err := NewEngine(Config{
		Network: NetworkConfig{
			Allow: []string{"app.vercel.app", "*.app.vercel.app", "docs.github.io"},
		},
	})
	if err != nil {
		t.Fatalf("NewEngine(specific shared-suffix hosts): %v", err)
	}
	for _, host := range []string{"app.vercel.app", "preview.app.vercel.app", "docs.github.io"} {
		if dec := eng.Decide(Request{Host: host, Port: 443}); dec.Action != ActionAllow {
			t.Errorf("Decide(%s) = %+v, want allow", host, dec)
		}
	}
	if dec := eng.Decide(Request{Host: "other.vercel.app", Port: 443}); dec.Action != ActionDeny {
		t.Errorf("Decide(other.vercel.app) = %+v, want deny", dec)
	}
}

func TestUploadBudgetRollingWindow(t *testing.T) {
	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	b := NewUploadBudgetWithClock(16<<20, func() time.Time { return now })

	allowed, exceeded, used, limit := b.Consume("paste.example", 10<<20)
	if allowed != 10<<20 || exceeded || used != 10<<20 || limit != 16<<20 {
		t.Fatalf("first consume = (%d, %v, %d, %d)", allowed, exceeded, used, limit)
	}
	now = now.Add(30 * time.Minute)
	allowed, exceeded, used, _ = b.Consume("paste.example", 10<<20)
	if allowed != 6<<20 || !exceeded || used != 16<<20 {
		t.Fatalf("second consume (20MiB total against 16MiB cap) = (%d, %v, %d), want (6MiB, true, 16MiB)", allowed, exceeded, used)
	}
	// Another host has an independent budget.
	if rem, _, _, lim := b.Remaining("other.example"); !lim || rem != 16<<20 {
		t.Fatalf("other.example remaining = %d (limited=%v), want 16MiB", rem, lim)
	}
	// After 61 minutes from the first upload, the first 10 MiB expire.
	now = now.Add(31 * time.Minute)
	if rem, used2, _, _ := b.Remaining("paste.example"); rem != 10<<20 || used2 != 6<<20 {
		t.Fatalf("after 1h expiry: rem=%d used=%d, want 10MiB/6MiB", rem, used2)
	}
}

func TestUploadBudgetStopsConnectAndHTTPUploads(t *testing.T) {
	// P2.7 acceptance: an upload of 20 MiB to a granted paste site stops at 16 MiB
	// (default upload_budget), and subsequent requests to that host are refused
	// with a Try: hint until upload_budget is raised.
	var received atomic.Int64
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	var acceptWG sync.WaitGroup
	acceptWG.Add(1)
	go func() {
		defer acceptWG.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				n, _ := io.Copy(io.Discard, conn)
				received.Add(n)
			}(c)
		}
	}()

	ap, err := NewAddressPolicyWithHosts(nil)
	if err != nil {
		t.Fatal(err)
	}
	fakePub := ip4(93, 184, 216, 34)
	res := &countingResolver{ips: map[string][]netip.Addr{
		"paste.example": {fakePub},
		"other.example": {fakePub},
	}}
	dial := func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, ln.Addr().String())
	}

	rec := &auditRecorder{}
	supConn, egConn := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		sd := &SafeDialer{Addrs: ap, Resolver: res, Dial: dial}
		done <- RunOnConn(ctx, "upload-budget-test", egConn, &ServerOptions{
			Addrs:  ap,
			Dialer: sd,
		})
	}()

	cli, err := NewClient(ctx, supConn, StartArgs{
		Session: "devbox/paste",
		PolicyTOML: `
[sandbox]
preset = "locked"
[network]
allow = ["paste.example:8080", "other.example:8080"]
`,
	}, rec.add)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		_ = cli.Close()
		<-done
		_ = ln.Close()
		acceptWG.Wait()
	})

	// 1. Open CONNECT tunnel to paste.example:8080 and try to upload 20 MiB.
	st, err := cli.OpenEgress(ctx, StreamHeader{Via: "explicit"})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(st, "CONNECT paste.example:8080 HTTP/1.1\r\nHost: paste.example:8080\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(st), &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("CONNECT err=%v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT status=%d", resp.StatusCode)
	}
	payload := bytes.Repeat([]byte("A"), 20<<20)
	written, werr := st.Write(payload)
	_ = st.Close()
	if werr == nil && written == len(payload) {
		t.Fatalf("expected 20 MiB upload to be stopped at 16 MiB cap, but all %d bytes wrote cleanly", written)
	}

	ev := rec.waitFor(t, func(e AuditEvent) bool {
		return e.Rule == "upload_budget" && e.Host == "paste.example" && e.Decision == string(ActionDeny)
	})
	if ev.Up != api.DefaultUploadBudgetBytes || ev.BudgetLimit != api.DefaultUploadBudgetBytes {
		t.Errorf("upload_budget audit event = %+v, want Up=16MiB BudgetLimit=16MiB", ev)
	}
	// Wait briefly for receiver goroutine to finish draining closed socket.
	deadline := time.Now().Add(2 * time.Second)
	for received.Load() > api.DefaultUploadBudgetBytes && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := received.Load(); got > api.DefaultUploadBudgetBytes {
		t.Errorf("upstream received %d bytes, exceeds 16 MiB cap", got)
	}

	// 2. Subsequent CONNECT to paste.example:8080 is refused immediately with 403 and Try: hint.
	st2, err := cli.OpenEgress(ctx, StreamHeader{Via: "explicit"})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(st2, "CONNECT paste.example:8080 HTTP/1.1\r\nHost: paste.example:8080\r\n\r\n")
	resp2, err := http.ReadResponse(bufio.NewReader(st2), &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("second CONNECT read response: %v", err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	_ = resp2.Body.Close()
	_ = st2.Close()
	if resp2.StatusCode != http.StatusForbidden || !strings.Contains(string(body2), "Try:") || !strings.Contains(string(body2), "upload_budget") {
		t.Errorf("second CONNECT = %d %q, want 403 with upload_budget and Try: hint", resp2.StatusCode, body2)
	}

	// 3. Raising upload_budget live via policy.reload lets paste.example connect again.
	if err := cli.Reload(ctx, "[sandbox]\npreset = \"locked\"\n[network]\nallow = [\"paste.example:8080\"]\nupload_budget = \"32M\"\n"); err != nil {
		t.Fatalf("Reload upload_budget=32M: %v", err)
	}
	st3, err := cli.OpenEgress(ctx, StreamHeader{Via: "explicit"})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(st3, "CONNECT paste.example:8080 HTTP/1.1\r\nHost: paste.example:8080\r\n\r\n")
	resp3, err := http.ReadResponse(bufio.NewReader(st3), &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("CONNECT after raise err=%v", err)
	}
	_ = resp3.Body.Close()
	if resp3.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT after raise status=%d, want 200", resp3.StatusCode)
	}
	_ = st3.Close()
}

func TestUploadBudgetHTTPForwardProxy(t *testing.T) {
	upSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upSrv.Close)

	ap, err := NewAddressPolicyWithHosts(nil)
	if err != nil {
		t.Fatal(err)
	}
	fakePub := ip4(93, 184, 216, 34)
	res := &countingResolver{ips: map[string][]netip.Addr{"upload.example": {fakePub}}}
	dial := func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, upSrv.Listener.Addr().String())
	}
	eng, err := NewEngine(Config{
		Session: "devbox/http-up",
		Sandbox: SandboxConfig{Preset: PresetLocked},
		Network: NetworkConfig{Allow: []string{"upload.example:80"}, UploadBudget: "4K"},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(ServerOptions{
		Engine: eng,
		Addrs:  ap,
		Dialer: &SafeDialer{Addrs: ap, Resolver: res, Dial: dial},
	})
	if err != nil {
		t.Fatal(err)
	}
	proxySrv := httptest.NewServer(srv)
	t.Cleanup(proxySrv.Close)

	proxyURL, err := url.Parse(proxySrv.URL)
	if err != nil {
		t.Fatal(err)
	}
	hc := &http.Client{
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		},
	}

	// 2 KiB POST succeeds.
	resp, err := hc.Post("http://upload.example/put", "application/octet-stream", bytes.NewReader(bytes.Repeat([]byte("x"), 2048)))
	if err != nil {
		t.Fatalf("2KiB POST: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("2KiB POST status = %d, want 200", resp.StatusCode)
	}

	// 4 KiB POST pushes total to 6 KiB > 4 KiB cap -> refused with 403 and Try: hint.
	resp, err = hc.Post("http://upload.example/put", "application/octet-stream", bytes.NewReader(bytes.Repeat([]byte("y"), 4096)))
	if err != nil {
		t.Fatalf("4KiB POST: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "Try:") {
		t.Errorf("4KiB POST = %d %q, want 403 with Try: hint", resp.StatusCode, body)
	}
}
