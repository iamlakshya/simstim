package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

func modelUsageDisplayFixture() *api.ModelUsageV1 {
	counts := api.ModelUsageCountsV1{Requests: 2, EstimatedRequests: 1, TotalTokens: 9007199254740993}
	return &api.ModelUsageV1{Version: 1, Scope: "sandbox", Total: counts, Unattributed: counts}
}

func TestShowUsageRequiresActualLiveSandboxCounterAndMarksEstimates(t *testing.T) {
	if got := showModelUsage(client.Session{Alive: true}); got != "" {
		t.Fatalf("missing producer gained zero usage: %q", got)
	}
	row := client.Session{ID: "usage234", Alive: true, Usage: modelUsageDisplayFixture()}
	text := describeSession("box", row)
	if !strings.Contains(text, "9007199254740993 model tokens (sandbox-wide), 1 estimated request") {
		t.Fatalf("actual shared usage/estimates missing: %q", text)
	}
	row.Alive = false
	if got := showModelUsage(row); got != "unknown (no live model usage)" {
		t.Fatalf("stopped row acquired current model use: %q", got)
	}
	row.Alive, row.Usage.Version = true, 2
	if got := showModelUsage(row); got != "unknown (no live model usage)" {
		t.Fatalf("future counter became current: %q", got)
	}
}

func TestSessionUsageJSONPreservesLargeCountersAndAbsentMeansAbsent(t *testing.T) {
	row := client.Session{ID: "usage234", Alive: true, Usage: modelUsageDisplayFixture()}
	data, err := versioned(sessionObject("box", row))
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Usage *api.ModelUsageV1 `json:"usage"`
	}
	if err := json.Unmarshal(data, &result); err != nil || result.Usage == nil || result.Usage.Total.TotalTokens != 9007199254740993 || result.Usage.Scope != "sandbox" {
		t.Fatalf("machine usage rounded or lost scope: %+v %v", result, err)
	}
	empty, err := versioned(sessionObject("box", client.Session{ID: "usage234"}))
	if err != nil || strings.Contains(string(empty), `"usage"`) {
		t.Fatalf("unsupported producer gained synthetic usage: %s %v", empty, err)
	}
}

func TestShowModelUsageKeepsExactTotalSeparateFromConservativeBudgetCharge(t *testing.T) {
	counts := api.ModelUsageCountsV1{Requests: 1, EstimatedRequests: 1, TotalTokens: 10}
	s := client.Session{Alive: true, Usage: &api.ModelUsageV1{Version: 1, Scope: "sandbox", Total: counts, Unattributed: counts, BudgetTokens: 50}}
	text := showModelUsage(s)
	if !strings.Contains(text, "10 model tokens") || !strings.Contains(text, "50 counted towards budget") || strings.Contains(text, "50 model tokens") {
		t.Fatal("budget floor replaced exact completed/cut ledger", text)
	}
}
