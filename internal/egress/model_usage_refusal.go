package egress

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
)

// ModelBudgetRefusalV1 is a decision from the actual live meter and current
// supplied budget. Alert is true only for its first refused budget check.
type ModelBudgetRefusalV1 struct {
	Wire      string
	Used      uint64
	Budget    int64
	Saturated bool
	Alert     bool
}

// LineV1 contains only scrubbed public context and counters, never payloads.
func (refusal *ModelBudgetRefusalV1) LineV1(session string) string {
	label := api.CleanText(session, 128)
	if label == "" {
		label = "this sandbox"
	}
	if refusal.Budget < 0 {
		return fmt.Sprintf("%s: %s has an invalid model-token budget. Try: set network.model_budget to a nonnegative value and apply the policy.", brand.Name, label)
	}
	if refusal.Saturated {
		return fmt.Sprintf("%s: %s reached the model-token accounting limit. Try: stop it and cold-start a new sandbox before sending more model requests.", brand.Name, label)
	}
	return fmt.Sprintf("%s: %s used %d of its %d model tokens. Try: raise network.model_budget with %s policy edit, then %s policy apply", brand.Name, label, refusal.Used, refusal.Budget, brand.Name, brand.Name)
}

// WriteHTTPV1 emits the provider's terminal budget refusal. It always removes
// Retry-After; a caller must not turn this response into a retryable delay.
func (refusal *ModelBudgetRefusalV1) WriteHTTPV1(writer http.ResponseWriter, session string) error {
	if refusal == nil || !validModelUsageWireV1(refusal.Wire) {
		return fmt.Errorf("invalid model budget refusal. Try: check the live model meter")
	}
	message := refusal.LineV1(session)
	var body any
	if refusal.Wire == "anthropic-messages" {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return fmt.Errorf("make model refusal request ID: %w. Try: check local random source availability", err)
		}
		body = map[string]any{"type": "error", "error": map[string]string{"type": "billing_error", "message": message}, "request_id": "req_" + hex.EncodeToString(random[:])}
	} else {
		body = map[string]any{"error": map[string]any{"message": message, "type": "insufficient_quota", "param": nil, "code": "insufficient_quota"}}
	}
	writer.Header().Del("Retry-After")
	writer.Header().Set("X-Should-Retry", "false")
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusTooManyRequests)
	if err := json.NewEncoder(writer).Encode(body); err != nil {
		return fmt.Errorf("write model budget refusal: %w. Try: check the current response connection", err)
	}
	return nil
}

// WebSocketCodeV1 is sent only after the current logical response finishes.
func (*ModelBudgetRefusalV1) WebSocketCodeV1() uint16 { return 1008 }
