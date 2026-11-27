package client

import (
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestModelUsageDisplayClonesActualCountersAndOmitsInvalidSnapshots(t *testing.T) {
	counts := api.ModelUsageCountsV1{Requests: 1, TotalTokens: 7}
	usage := &api.ModelUsageV1{Version: 1, Scope: "sandbox", Total: counts, Models: []api.ModelUsageModelV1{{Protocol: "openai-responses", Model: "model-a", Counts: counts}}}
	row := Session{ID: "usage234", Alive: true, Usage: usage}
	drawn := CleanSession(row)
	if drawn.Usage == nil || drawn.Usage == usage || &drawn.Usage.Models[0] == &usage.Models[0] {
		t.Fatal("drawn usage borrowed live producer memory")
	}
	drawn.Usage.Models[0].Counts.TotalTokens = 0
	if usage.Models[0].Counts.TotalTokens != 7 || row.Usage.Total.TotalTokens != 7 {
		t.Fatal("drawing changed the raw counter")
	}
	bad := *usage
	bad.Models = append([]api.ModelUsageModelV1(nil), usage.Models...)
	bad.Models[0].Model = "model\x1b[31m"
	row.Usage = &bad
	if CleanSession(row).Usage != nil || bad.Models[0].Model != "model\x1b[31m" {
		t.Fatal("invalid raw usage was normalised into valid producer data")
	}
}
