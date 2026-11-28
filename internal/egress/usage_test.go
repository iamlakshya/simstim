package egress

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestUsageCountingStreamsAndEstimates(t *testing.T) {
	const anthropicSSE = "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_1","model":"claude-sonnet-4-6","usage":{"input_tokens":100,"cache_creation_input_tokens":20,"cache_read_input_tokens":30,"output_tokens":1}}}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","usage":{"output_tokens":15}}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","usage":{"output_tokens":40,"server_tool_use":{"web_search_requests":2}}}` + "\n\n"

	const openAIResponsesSSE = "event: response.created\n" +
		`data: {"type":"response.created","response":{"id":"r1","model":"gpt-5"}}` + "\n\n" +
		"event: response.completed\n" +
		`data: {"type":"response.completed","response":{"id":"r1","model":"gpt-5","usage":{"input_tokens":200,"input_tokens_details":{"cached_tokens":50},"output_tokens":60,"output_tokens_details":{"reasoning_tokens":10},"total_tokens":260}}}` + "\n\n"

	const openAIChatWithUsageSSE = `data: {"id":"c1","model":"gpt-4.1","choices":[{"delta":{"content":"hi"}}],"usage":null}` + "\n\n" +
		`data: {"id":"c1","model":"gpt-4.1","choices":[],"usage":{"prompt_tokens":30,"completion_tokens":12,"total_tokens":42}}` + "\n\n" +
		"data: [DONE]\n\n"

	const openAIChatNoUsageSSE = `data: {"id":"c2","model":"gpt-4.1","choices":[{"delta":{"content":"hello world"}}]}` + "\n\n" +
		"data: [DONE]\n\n"

	upSrv, pool := startMockModelUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/messages":
			w.Header().Set("Content-Type", "text/event-stream")
			if r.URL.Query().Get("cut") == "1" {
				_, _ = io.WriteString(w, "event: content_block_delta\ndata: {\"delta\":{\"text\":\"partial\"}}\n\n")
				return
			}
			_, _ = io.WriteString(w, anthropicSSE)
		case "/v1/messages/count_tokens":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"input_tokens":999}`)
		case "/v1/responses":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, openAIResponsesSSE)
		case "/v1/chat/completions":
			w.Header().Set("Content-Type", "text/event-stream")
			if r.URL.Query().Get("usage") == "1" {
				_, _ = io.WriteString(w, openAIChatWithUsageSSE)
				return
			}
			_, _ = io.WriteString(w, openAIChatNoUsageSSE)
		}
	}, "api.anthropic.com", "api.openai.com")

	cli, rec, hc := startGatewayTestEgress(t, StartArgs{
		Session:    "devbox/usage",
		PolicyTOML: "[sandbox]\npreset = \"balanced\"\n",
		BoundSecrets: []BoundCredential{
			{Route: "anthropic", Name: "ANTHROPIC_API_KEY", Header: "x-api-key", Value: api.Secret("k1")},
			{Route: "openai", Name: "OPENAI_API_KEY", Header: "Authorization", Format: "Bearer {value}", Value: api.Secret("k2")},
		},
	}, upSrv.Listener.Addr().String(), pool)

	post := func(url, body string) {
		t.Helper()
		resp, err := hc.Post(url, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("POST %s: %v", url, err)
		}
		_, _ = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("POST %s status = %d", url, resp.StatusCode)
		}
	}

	// 1. Anthropic SSE: cumulative message_delta counts output_tokens=40 ONCE (total = 100+20+30+40 = 190).
	post("http://127.0.0.1:3128/gw/anthropic/v1/messages", `{"stream":true}`)
	// 2. count_tokens counts NOTHING (spec 17.8).
	post("http://127.0.0.1:3128/gw/anthropic/v1/messages/count_tokens", `{"messages":[]}`)
	// 3. OpenAI Responses SSE: total_tokens = 260, cached = 50, reasoning = 10.
	post("http://127.0.0.1:3128/gw/openai/v1/responses", `{"stream":true}`)
	// 4. OpenAI Chat Completions SSE with include_usage: total_tokens = 42.
	post("http://127.0.0.1:3128/gw/openai/v1/chat/completions?usage=1", `{"stream":true}`)
	// 5. OpenAI Chat Completions SSE without usage -> byte estimate.
	chatReqBody := `{"stream":true,"messages":[]}`
	post("http://127.0.0.1:3128/gw/openai/v1/chat/completions", chatReqBody)
	// 6. Cut Anthropic SSE stream without usage -> byte estimate.
	cutReqBody := `{"stream":true,"cut":true}`
	post("http://127.0.0.1:3128/gw/anthropic/v1/messages?cut=1", cutReqBody)

	summary, err := cli.Usage(context.Background())
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	wantChatEst := int64(len(chatReqBody)+len(openAIChatNoUsageSSE)+3) / 4
	wantCutResp := len("event: content_block_delta\ndata: {\"delta\":{\"text\":\"partial\"}}\n\n")
	wantCutEst := int64(len(cutReqBody)+wantCutResp+3) / 4

	if got := summary.ByModel["claude-sonnet-4-6"]; got.TotalTokens != 190 || got.OutputTokens != 40 || got.WebSearchRequests != 2 {
		t.Errorf("claude-sonnet-4-6 usage = %+v, want total=190 output=40 search=2", got)
	}
	if got := summary.ByModel["gpt-5"]; got.TotalTokens != 260 || got.CacheReadTokens != 50 || got.ReasoningTokens != 10 {
		t.Errorf("gpt-5 usage = %+v, want total=260 cached=50 reasoning=10", got)
	}
	wantTotal := 190 + 260 + 42 + wantChatEst + wantCutEst
	if summary.Total.TotalTokens != wantTotal || summary.Total.Requests != 5 || summary.Total.EstimatedRequests != 2 {
		t.Errorf("total usage = %+v, want total=%d requests=5 estimated=2", summary.Total, wantTotal)
	}

	// Verify audit event marks estimates as "usage":"estimate".
	estEv := rec.waitFor(t, func(e AuditEvent) bool {
		return e.Mode == "gateway" && e.UsageKind == "estimate" && e.Host == "api.openai.com"
	})
	if estEv.Usage == nil || !estEv.Usage.Estimated || estEv.Usage.TotalTokens != wantChatEst {
		t.Errorf("estimate audit event = %+v, want estimated=%d", estEv, wantChatEst)
	}
}

func TestUsageObserverCapsOversizedSSEEvent(t *testing.T) {
	obs := newUsageObserver(WireOpenAIResponses, 16, "text/event-stream")
	bigLine := "data: " + strings.Repeat("x", 1<<20) + "\n"
	obs.Write([]byte("event: response.output_text.delta\n"))
	for range 10 {
		obs.Write([]byte(bigLine))
	}
	if !obs.sseDataTooBig || len(obs.sseData) != 0 {
		t.Fatalf("expected oversized SSE event to trip cap and drop buffer: tooBig=%v len=%d", obs.sseDataTooBig, len(obs.sseData))
	}
	// End the oversized event and send a normal completion event; usage must still parse.
	obs.Write([]byte("\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"model\":\"gpt-5\",\"usage\":{\"input_tokens\":10,\"output_tokens\":5,\"total_tokens\":15}}}\n\n"))
	got := obs.Finish()
	if got.Estimated || got.TotalTokens != 15 || got.Model != "gpt-5" {
		t.Fatalf("Finish() = %+v, want exact total=15 model=gpt-5", got)
	}
}

func FuzzUsageSSE(f *testing.F) {
	f.Add("anthropic-messages", []byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":5}}}\n\n"))
	f.Add("openai-responses", []byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"total_tokens\":9}}}\n\n"))
	f.Fuzz(func(_ *testing.T, wire string, data []byte) {
		obs := newUsageObserver(wire, 16, "text/event-stream")
		obs.Write(data)
		_ = obs.Finish()
	})
}
