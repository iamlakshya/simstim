package egress

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func finishReportedUsage(t *testing.T, meter *ModelUsageMeterV1, model string, tokens uint64) {
	t.Helper()
	turn, refusal, err := meter.BeginV1(ModelUsageRequestV1{Wire: "openai-responses", Path: "/v1/responses", Model: model}, 0)
	if err != nil || refusal != nil {
		t.Fatalf("begin: %v %v", err, refusal)
	}
	turn.ForwardedV1([]byte(fmt.Sprintf(`{"usage":{"total_tokens":%d}}`, tokens)))
	if completion := turn.FinishV1(true); completion.Estimated || completion.Counts.TotalTokens != tokens {
		t.Fatalf("reported usage: %+v", completion)
	}
}

func TestModelUsageCardinalityAndAdversarialLabelsNeverLoseGrandTotals(t *testing.T) {
	meter := NewModelUsageMeterV1()
	for index := 0; index < api.ModelUsageModelLimitV1+9; index++ {
		finishReportedUsage(t, meter, fmt.Sprintf("model-%03d", index), 1)
	}
	finishReportedUsage(t, meter, "model-000", 5)
	finishReportedUsage(t, meter, strings.Repeat("x", 4096), 7)
	finishReportedUsage(t, meter, "", 3)
	got := meter.SnapshotV1()
	if len(got.Models) != api.ModelUsageModelLimitV1 || got.Total.Requests != 140 || got.Total.TotalTokens != 152 || got.Unattributed.Requests != 11 || got.Unattributed.TotalTokens != 19 || !api.ValidModelUsageV1(got) {
		t.Fatalf("cardinality changed billing or evicted a bucket: %+v", got)
	}
	found := false
	for _, model := range got.Models {
		if model.Model == "model-000" {
			found = model.Counts.Requests == 2 && model.Counts.TotalTokens == 6
		}
	}
	if !found {
		t.Fatal("a full breakdown evicted the original model instead of counting it")
	}
	// Different private labels that draw alike remain different accounts.
	other := NewModelUsageMeterV1()
	finishReportedUsage(t, other, "model\u200b", 2)
	finishReportedUsage(t, other, "model\u202e", 3)
	labels := other.SnapshotV1()
	if len(labels.Models) != 2 || labels.Models[0].Model != "model" || labels.Models[1].Model != "model" || labels.Total.TotalTokens != 5 || !api.ValidModelUsageV1(labels) {
		t.Fatalf("drawn labels became lookup keys: %+v", labels)
	}
	labels.Models[0].Counts.TotalTokens = 0
	if other.SnapshotV1().Total.TotalTokens != 5 {
		t.Fatal("public snapshot mutation changed the live meter")
	}
}

func TestModelUsageInflightCompletesAndRaisedCurrentBudgetAppliesNextRequest(t *testing.T) {
	meter := NewModelUsageMeterV1()
	request := ModelUsageRequestV1{Wire: "openai-responses", Path: "/v1/responses", Model: "model-a"}
	first, refusal, err := meter.BeginV1(request, 5)
	if err != nil || refusal != nil {
		t.Fatal(err)
	}
	second, refusal, err := meter.BeginV1(request, 5)
	if err != nil || refusal != nil {
		t.Fatal("inflight was treated as unreported reserved tokens", err)
	}
	first.ForwardedV1([]byte(`{"usage":{"total_tokens":10}}`))
	first.FinishV1(true)
	second.ForwardedV1([]byte(`{"usage":{"total_tokens":11}}`))
	second.FinishV1(true)
	if meter.SnapshotV1().Total.TotalTokens != 21 {
		t.Fatal("an inflight request was cut off after another crossed budget")
	}
	if _, refusal, err := meter.BeginV1(request, 5); err != nil || refusal == nil || !refusal.Alert || refusal.Used != 21 {
		t.Fatalf("current budget did not refuse once: %+v %v", refusal, err)
	}
	if again := meter.BudgetRefusalV1(request.Wire, 5); again == nil || again.Alert || again.WebSocketCodeV1() != 1008 {
		t.Fatal("budget alert repeated or WS refusal changed")
	}
	if turn, refused, err := meter.BeginV1(request, 100); err != nil || refused != nil || turn == nil {
		t.Fatalf("raised live budget did not apply: %v %+v", err, refused)
	}
}

func TestModelUsageFinishIsOnceOnlyUnderCancellationAndEOFRaces(t *testing.T) {
	meter, turn := usageTurnFixture(t, "openai-responses", "model-a", 1)
	turn.ForwardedV1([]byte(`{"usage":{"total_tokens":7}}`))
	var workers sync.WaitGroup
	for range 16 {
		workers.Go(func() { turn.FinishV1(true) })
	}
	workers.Wait()
	if got := meter.SnapshotV1().Total; got.Requests != 1 || got.TotalTokens != 7 {
		t.Fatalf("one logical answer committed repeatedly: %+v", got)
	}
}

func TestModelUsageCheckedOverflowCannotWrapIntoFreeQuota(t *testing.T) {
	meter, turn := usageTurnFixture(t, "anthropic-messages", "model-a", 0)
	turn.ForwardedV1([]byte(`{"usage":{"input_tokens":18446744073709551615,"output_tokens":1}}`))
	completion := turn.FinishV1(true)
	got := meter.SnapshotV1()
	if !completion.Saturated || !got.Saturated || got.Total.TotalTokens != math.MaxUint64 || !api.ValidModelUsageV1(got) {
		t.Fatalf("token sum wrapped: %+v %+v", completion, got)
	}
	if _, refusal, err := meter.BeginV1(ModelUsageRequestV1{Wire: "openai-responses", Path: "/v1/responses", Model: "model-a"}, 0); err != nil || refusal == nil || !refusal.Saturated {
		t.Fatalf("saturated counter created unlimited free quota: %+v %v", refusal, err)
	}
	if estimate, overflow := modelUsageEstimateV1(math.MaxUint64, math.MaxUint64); overflow || estimate != 1<<63 {
		t.Fatalf("estimate used unchecked additive byte sizing: %d %v", estimate, overflow)
	}
}

func TestModelBudgetRefusalsAreProviderExactAndNeverRetryable(t *testing.T) {
	for _, wire := range []string{"anthropic-messages", "openai-responses", "openai-chat-completions"} {
		refusal := &ModelBudgetRefusalV1{Wire: wire, Used: 2000412, Budget: 2000000}
		recorder := httptest.NewRecorder()
		recorder.Header().Set("Retry-After", "1")
		if err := refusal.WriteHTTPV1(recorder, "box/work\u202e"); err != nil {
			t.Fatal(err)
		}
		if recorder.Code != http.StatusTooManyRequests || recorder.Header().Get("X-Should-Retry") != "false" || recorder.Header().Get("Retry-After") != "" || strings.ContainsRune(recorder.Body.String(), '\u202e') {
			t.Fatalf("retry or unsafe header/body: %d %v %q", recorder.Code, recorder.Header(), recorder.Body.String())
		}
		var body struct {
			Type      string `json:"type"`
			RequestID string `json:"request_id"`
			Error     struct {
				Type    string `json:"type"`
				Code    string `json:"code"`
				Message string `json:"message"`
				Param   any    `json:"param"`
			} `json:"error"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if wire == "anthropic-messages" {
			if body.Type != "error" || body.Error.Type != "billing_error" || !strings.HasPrefix(body.RequestID, "req_") {
				t.Fatalf("Anthropic refusal: %+v", body)
			}
		} else if body.Error.Type != "insufficient_quota" || body.Error.Code != "insufficient_quota" || body.Error.Param != nil {
			t.Fatalf("OpenAI refusal: %+v", body)
		}
		if !strings.Contains(body.Error.Message, "2000412 of its 2000000 model tokens") || !strings.Contains(body.Error.Message, "Try:") {
			t.Fatalf("budget counters/recovery hint missing: %q", body.Error.Message)
		}
	}
}

func TestModelUsageDiagnosticsDoNotExposeBufferedContentOrPrivateLabels(t *testing.T) {
	meter, turn := usageTurnFixture(t, "openai-responses", "private-model-label", 0)
	turn.ForwardedV1([]byte(`{"output":"private-model-answer"}`))
	for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
		for _, value := range []any{ModelUsageRequestV1{Model: "private-model-label"}, turn, meter} {
			if text := fmt.Sprintf(verb, value); strings.Contains(text, "private-model") {
				t.Fatalf("diagnostic exposed model content or identity: %q", text)
			}
		}
	}
}
