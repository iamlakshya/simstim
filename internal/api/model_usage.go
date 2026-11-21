package api

import "math"

// ModelUsageVersionV1 is the live sandbox-wide model accounting schema.
const ModelUsageVersionV1 = 1

// FeatureModelUsageV1 is advertised only after actual HTTP and WebSocket hooks.
const FeatureModelUsageV1 = "model-usage-v1"

// ModelUsageModelLimitV1 bounds only the descriptive model breakdown.
// Extra labels remain charged to the explicit unattributed bucket.
const ModelUsageModelLimitV1 = 128

// ModelUsageCountsV1 contains counters, never a price or approval.
// TotalTokens is provider-normalised: Anthropic sums its four charge fields;
// OpenAI supplies total_tokens because input already includes cached tokens.
type ModelUsageCountsV1 struct {
	Requests                 uint64 `json:"requests"`
	EstimatedRequests        uint64 `json:"estimated_requests"`
	InputTokens              uint64 `json:"input_tokens"`
	OutputTokens             uint64 `json:"output_tokens"`
	CacheCreationInputTokens uint64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     uint64 `json:"cache_read_input_tokens"`
	CachedInputTokens        uint64 `json:"cached_input_tokens"`
	ReasoningTokens          uint64 `json:"reasoning_tokens"`
	WebSearchRequests        uint64 `json:"web_search_requests"`
	TotalTokens              uint64 `json:"total_tokens"`
}

// ModelUsageModelV1 describes one bounded model bucket. Labels are display
// text only; a cleaned label never merges accounts or controls a budget.
type ModelUsageModelV1 struct {
	Protocol string             `json:"protocol"`
	Model    string             `json:"model"`
	Redacted bool               `json:"redacted,omitempty"`
	Counts   ModelUsageCountsV1 `json:"counts"`
}

// ModelUsageV1 is an actual live Server/root-eg meter snapshot. The current
// stream header supplies no trusted per-member attribution, so scope is
// sandbox. A DTO alone cannot construct an active meter or allowance.
type ModelUsageV1 struct {
	Version int                `json:"v"`
	Scope   string             `json:"scope"`
	Total   ModelUsageCountsV1 `json:"total"`
	// BudgetTokens includes known in-flight counts and a known charge floor
	// for cut answers. Total keeps the documented fallback byte estimate.
	BudgetTokens uint64              `json:"budget_tokens,omitempty"`
	Models       []ModelUsageModelV1 `json:"models,omitempty"`
	Unattributed ModelUsageCountsV1  `json:"unattributed"`
	Saturated    bool                `json:"saturated,omitempty"`
}

// AddModelUsageCountsV1 adds counters without ever wrapping. Saturation is
// explicit and must never make a live budget appear to have quota left.
func AddModelUsageCountsV1(a, b ModelUsageCountsV1) (ModelUsageCountsV1, bool) {
	saturated := false
	add := func(x, y uint64) uint64 {
		if y > math.MaxUint64-x {
			saturated = true
			return math.MaxUint64
		}
		return x + y
	}
	return ModelUsageCountsV1{
		Requests: add(a.Requests, b.Requests), EstimatedRequests: add(a.EstimatedRequests, b.EstimatedRequests),
		InputTokens: add(a.InputTokens, b.InputTokens), OutputTokens: add(a.OutputTokens, b.OutputTokens),
		CacheCreationInputTokens: add(a.CacheCreationInputTokens, b.CacheCreationInputTokens), CacheReadInputTokens: add(a.CacheReadInputTokens, b.CacheReadInputTokens),
		CachedInputTokens: add(a.CachedInputTokens, b.CachedInputTokens), ReasoningTokens: add(a.ReasoningTokens, b.ReasoningTokens),
		WebSearchRequests: add(a.WebSearchRequests, b.WebSearchRequests), TotalTokens: add(a.TotalTokens, b.TotalTokens),
	}, saturated
}

// ValidModelUsageV1 validates a complete bounded display snapshot. Its
// counters are advisory data; actual budget admission uses the live meter.
func ValidModelUsageV1(snapshot ModelUsageV1) bool {
	if snapshot.Version != ModelUsageVersionV1 || snapshot.Scope != "sandbox" || len(snapshot.Models) > ModelUsageModelLimitV1 || snapshot.Total.EstimatedRequests > snapshot.Total.Requests || snapshot.Unattributed.EstimatedRequests > snapshot.Unattributed.Requests || snapshot.BudgetTokens != 0 && snapshot.BudgetTokens < snapshot.Total.TotalTokens {
		return false
	}
	sum, saturated := snapshot.Unattributed, false
	for _, model := range snapshot.Models {
		if model.Protocol != "anthropic-messages" && model.Protocol != "openai-responses" && model.Protocol != "openai-chat-completions" || model.Model == "" || CleanText(model.Model, 96) != model.Model || model.Counts.EstimatedRequests > model.Counts.Requests {
			return false
		}
		var overflow bool
		sum, overflow = AddModelUsageCountsV1(sum, model.Counts)
		saturated = saturated || overflow
	}
	return sum == snapshot.Total && (!saturated || snapshot.Saturated)
}
