package host

import (
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestCheckWalledSettingsBudgetsAndPolicyTOML(t *testing.T) {
	ws, err := checkWalledSettings(NewArgs{
		PackageManagers: "guarded",
		MinReleaseAge:   "7d",
		ModelBudget:     250000,
		UploadBudget:    "8M",
	})
	if err != nil {
		t.Fatalf("checkWalledSettings(valid): %v", err)
	}
	if ws.modelBudget != 250000 || ws.uploadBudget != "8M" {
		t.Fatalf("ws = %+v", ws)
	}
	toml := buildSessionPolicyTOML("balanced", ws)
	for _, want := range []string{`preset = "balanced"`, `model_budget = 250000`, `upload_budget = "8M"`, `min_release_age = "7d"`} {
		if !strings.Contains(toml, want) {
			t.Errorf("policy TOML missing %q:\n%s", want, toml)
		}
	}

	if _, err := checkWalledSettings(NewArgs{ModelBudget: -5}); err == nil {
		t.Error("expected error for negative model_budget")
	}
	if _, err := checkWalledSettings(NewArgs{UploadBudget: "bad"}); err == nil {
		t.Error("expected error for invalid upload_budget")
	}

	// infoFrom clones Usage onto SessionInfo without sharing the map.
	m := &Manifest{
		ID: "abcd2345", Name: "budget-test",
		Usage: &api.SessionUsage{
			Total:   api.TokenUsage{TotalTokens: 120, Requests: 1},
			ByModel: map[string]api.TokenUsage{"gpt-5": {Model: "gpt-5", TotalTokens: 120, Requests: 1}},
			Budget:  500,
		},
	}
	info := infoFrom(m, false)
	if info.Usage == nil || info.Usage.Total.TotalTokens != 120 || info.Usage.Budget != 500 {
		t.Fatalf("infoFrom Usage = %+v", info.Usage)
	}
	info.Usage.ByModel["gpt-5"] = api.TokenUsage{TotalTokens: 9999}
	if m.Usage.ByModel["gpt-5"].TotalTokens != 120 {
		t.Error("infoFrom shared ByModel map with Manifest")
	}
}
