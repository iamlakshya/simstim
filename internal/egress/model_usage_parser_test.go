package egress

import (
	"fmt"
	"strings"
	"testing"
)

func usageTurnFixture(t *testing.T, wire, model string, requestBytes uint64) (*ModelUsageMeterV1, *ModelUsageTurnV1) {
	t.Helper()
	path := "/v1/responses"
	if wire == "anthropic-messages" {
		path = "/v1/messages"
	} else if wire == "openai-chat-completions" {
		path = "/v1/chat/completions"
	}
	meter := NewModelUsageMeterV1()
	turn, refused, err := meter.BeginV1(ModelUsageRequestV1{Wire: wire, Path: path, Model: model, RequestBytes: requestBytes}, 0)
	if err != nil || refused != nil || turn == nil {
		t.Fatalf("begin=%v refusal=%v", err, refused)
	}
	return meter, turn
}

func feedUsageChunks(turn *ModelUsageTurnV1, body string, width int) {
	for len(body) > 0 {
		count := min(width, len(body))
		turn.ForwardedV1([]byte(body[:count]))
		body = body[count:]
	}
}

func TestAnthropicUsageReplacesCumulativeFieldsInsteadOfAddingDeltas(t *testing.T) {
	meter, turn := usageTurnFixture(t, "anthropic-messages", "model-a", 9)
	turn.ResponseV1(ModelUsageSSEV1)
	body := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10,\"cache_creation_input_tokens\":20,\"cache_read_input_tokens\":30,\"output_tokens\":1}}}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":4,\"server_tool_use\":{\"web_search_requests\":2}}}\n\n" +
		"data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":7,\"server_tool_use\":{\"web_search_requests\":5}}}\n\n" +
		"data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":7}}\n\ndata: {\"type\":\"message_stop\"}\n\n"
	feedUsageChunks(turn, body, 1)
	completion := turn.FinishV1(true)
	got := meter.SnapshotV1()
	if completion.Estimated || got.Total.TotalTokens != 67 || got.Total.InputTokens != 10 || got.Total.OutputTokens != 7 || got.Total.WebSearchRequests != 5 || got.Total.Requests != 1 {
		t.Fatalf("cumulative usage was added or lost: %+v", got.Total)
	}
}

func TestOpenAIResponseTerminalUsageCountsTotalOnceForEveryOutcome(t *testing.T) {
	for _, terminal := range []string{"completed", "incomplete", "failed"} {
		t.Run(terminal, func(t *testing.T) {
			meter, turn := usageTurnFixture(t, "openai-responses", "model-a", 7)
			turn.ResponseV1(ModelUsageSSEV1)
			event := fmt.Sprintf(`{"type":"response.%s","response":{"usage":{"input_tokens":100,"input_tokens_details":{"cached_tokens":40},"output_tokens":20,"output_tokens_details":{"reasoning_tokens":10},"total_tokens":120}}}`, terminal)
			feedUsageChunks(turn, "data: "+event+"\n\ndata: "+event+"\n\n", 7)
			if completion := turn.FinishV1(true); completion.Estimated || completion.Counts.TotalTokens != 120 {
				t.Fatalf("terminal usage counted cached/deltas twice: %+v", completion)
			}
			if got := meter.SnapshotV1().Total; got.CachedInputTokens != 40 || got.ReasoningTokens != 10 || got.Requests != 1 {
				t.Fatalf("terminal detail fields changed: %+v", got)
			}
		})
	}
}

func TestChatCompletionsUsageAndMissingUsageKeepTheOriginalStreamBytes(t *testing.T) {
	for _, reported := range []bool{false, true} {
		meter, turn := usageTurnFixture(t, "openai-chat-completions", "model-a", 11)
		turn.ResponseV1(ModelUsageSSEV1)
		body := "data: {\"usage\":null,\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n"
		if reported {
			body += "data: {\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":20,\"total_tokens\":120}}\n\n"
		}
		body += "data: [DONE]\n\n"
		feedUsageChunks(turn, body, 13)
		got := turn.FinishV1(true)
		if reported && (got.Estimated || got.Counts.TotalTokens != 120) {
			t.Fatalf("reported chat usage: %+v", got)
		}
		want, _ := modelUsageEstimateV1(11, uint64(len(body)))
		if !reported && (!got.Estimated || got.Counts.TotalTokens != want || meter.SnapshotV1().Total.EstimatedRequests != 1) {
			t.Fatalf("SSE lines were omitted or usage was invented: %+v", got)
		}
	}
}

func TestCutResponseUsesEstimateEvenAfterAnEarlierUsageEvent(t *testing.T) {
	_, turn := usageTurnFixture(t, "anthropic-messages", "model-a", 5)
	turn.ResponseV1(ModelUsageSSEV1)
	body := "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":999,\"output_tokens\":1}}}\n\n"
	feedUsageChunks(turn, body, 9)
	want, _ := modelUsageEstimateV1(5, uint64(len(body)))
	if got := turn.FinishV1(false); !got.Estimated || got.Counts.TotalTokens != want {
		t.Fatalf("cut stream promoted partial usage: %+v", got)
	}
}

func TestWebSocketUsageCountsMessagePayloadsAndOneLogicalTerminal(t *testing.T) {
	meter, turn := usageTurnFixture(t, "openai-responses", "model-a", 17)
	turn.ResponseV1(ModelUsageWebSocketV1)
	body := `{"type":"response.output_text.delta","delta":"hello"}` +
		`{"type":"response.completed","response":{"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}}`
	feedUsageChunks(turn, body, 3)
	if got := turn.FinishV1(true); got.Estimated || got.Counts.TotalTokens != 5 || meter.SnapshotV1().Total.Requests != 1 {
		t.Fatalf("WS terminal usage: %+v", got)
	}
	_, cut := usageTurnFixture(t, "openai-responses", "model-a", 17)
	cut.ResponseV1(ModelUsageWebSocketV1)
	feedUsageChunks(cut, body, 11)
	want, _ := modelUsageEstimateV1(17, uint64(len(body)))
	if got := cut.FinishV1(false); !got.Estimated || got.Counts.TotalTokens != want {
		t.Fatalf("WS cut omitted payloads or framing estimate changed: %+v", got)
	}
}

func TestMalformedUsageNumbersAndAliasesDoNotBecomeReportedTokens(t *testing.T) {
	for _, number := range []string{"-1", "1.5", "1e4", `"12"`, "18446744073709551616"} {
		_, turn := usageTurnFixture(t, "openai-responses", "model-a", 5)
		body := `{"usage":{"total_tokens":` + number + `}}`
		feedUsageChunks(turn, body, 4)
		want, _ := modelUsageEstimateV1(5, uint64(len(body)))
		if got := turn.FinishV1(true); !got.Estimated || got.Counts.TotalTokens != want {
			t.Fatalf("malformed count %q was promoted: %+v", number, got)
		}
	}
	for _, body := range []string{`{"usage":{"total_tokens":100,"total_tokens":1}}`, `{"usage":{"total_tokens":100},"Usage":{"total_tokens":1}}`} {
		_, turn := usageTurnFixture(t, "openai-responses", "model-a", 5)
		turn.ForwardedV1([]byte(body))
		if got := turn.FinishV1(true); !got.Estimated {
			t.Fatalf("ambiguous usage became reported tokens: %+v", got)
		}
	}
}

func TestModelUsageIgnoresCountTokensAndDiscoveryWithoutChangingPayloads(t *testing.T) {
	meter := NewModelUsageMeterV1()
	for _, request := range []ModelUsageRequestV1{{Wire: "anthropic-messages", Path: "/v1/messages/count_tokens", Model: "model-a"}, {Wire: "openai-responses", Path: "/v1/models", Model: "model-a"}} {
		turn, refusal, err := meter.BeginV1(request, 1)
		if err != nil || refusal != nil {
			t.Fatal(err)
		}
		payload := []byte(strings.Repeat("content", 32))
		before := string(payload)
		turn.ForwardedV1(payload)
		turn.FinishV1(true)
		if string(payload) != before {
			t.Fatal("observer changed model payload bytes")
		}
	}
	if got := meter.SnapshotV1().Total; got.Requests != 0 || got.TotalTokens != 0 || got.EstimatedRequests != 0 {
		t.Fatalf("count_tokens/discovery charged inference: %+v", got)
	}
}
