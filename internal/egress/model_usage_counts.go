package egress

import (
	"math"

	"github.com/iamlakshya/simstim/internal/api"
)

func (parser *modelUsageParserV1) reportedCounts() (api.ModelUsageCountsV1, bool, bool) {
	fields := parser.fields
	counts := api.ModelUsageCountsV1{
		InputTokens: fields.Input.value, OutputTokens: fields.Output.value,
		CacheCreationInputTokens: fields.CacheWrite.value, CacheReadInputTokens: fields.CacheRead.value,
		CachedInputTokens: fields.InputDetails.Cached.value, ReasoningTokens: fields.OutputDetails.Reasoning.value,
		WebSearchRequests: fields.ServerTools.Search.value,
	}
	if parser.wire == "anthropic-messages" {
		saturated := false
		for _, value := range []uint64{counts.InputTokens, counts.OutputTokens, counts.CacheCreationInputTokens, counts.CacheReadInputTokens} {
			if value > math.MaxUint64-counts.TotalTokens {
				counts.TotalTokens, saturated = math.MaxUint64, true
			} else {
				counts.TotalTokens += value
			}
		}
		return counts, true, saturated
	}
	if !fields.Total.present {
		return api.ModelUsageCountsV1{}, false, false
	}
	counts.TotalTokens = fields.Total.value
	if parser.wire == "openai-chat-completions" {
		counts.InputTokens, counts.OutputTokens = fields.Prompt.value, fields.Completion.value
		counts.CachedInputTokens, counts.ReasoningTokens = fields.PromptDetails.Cached.value, fields.CompletionDetails.Reasoning.value
	}
	return counts, true, false
}
