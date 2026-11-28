package api

import (
	"strings"
	"testing"
)

func TestCheckModelBudget(t *testing.T) {
	for _, ok := range []int64{0, 1, 2_000_000} {
		if err := CheckModelBudget(ok); err != nil {
			t.Errorf("CheckModelBudget(%d) = %v, want nil", ok, err)
		}
	}
	err := CheckModelBudget(-10)
	if err == nil || !strings.Contains(err.Error(), "Try:") {
		t.Fatalf("CheckModelBudget(-10) = %v, want error with Try: hint", err)
	}
}

func TestParseUploadBudget(t *testing.T) {
	cases := []struct {
		in        string
		wantBytes int64
		wantCanon string
	}{
		{"", DefaultUploadBudgetBytes, DefaultUploadBudget},
		{"  ", DefaultUploadBudgetBytes, DefaultUploadBudget},
		{"0", 0, "0"},
		{"0M", 0, "0"},
		{"512B", 512, "512B"},
		{"64K", 64 << 10, "64K"},
		{"16M", 16 << 20, "16M"},
		{"16MiB", 16 << 20, "16MiB"},
		{"1.5G", int64(1.5 * float64(1<<30)), "1.5G"},
	}
	for _, tc := range cases {
		b, canon, err := ParseUploadBudget(tc.in)
		if err != nil || b != tc.wantBytes || canon != tc.wantCanon {
			t.Errorf("ParseUploadBudget(%q) = (%d, %q, %v); want (%d, %q, nil)",
				tc.in, b, canon, err, tc.wantBytes, tc.wantCanon)
		}
	}
	for _, bad := range []string{"-1M", "16", "unlimited", "90s", "16Z", "999999999999T"} {
		if _, _, err := ParseUploadBudget(bad); err == nil || !strings.Contains(err.Error(), "Try:") {
			t.Errorf("ParseUploadBudget(%q) = %v, want error with Try: hint", bad, err)
		}
	}
}

func TestSessionUsageClone(t *testing.T) {
	var nilUsage *SessionUsage
	if nilUsage.Clone() != nil {
		t.Fatal("nil Clone should return nil")
	}
	orig := &SessionUsage{
		Total:   TokenUsage{TotalTokens: 100, Requests: 1},
		ByModel: map[string]TokenUsage{"gpt-5": {Model: "gpt-5", TotalTokens: 100, Requests: 1}},
		Budget:  500,
	}
	cp := orig.Clone()
	cp.ByModel["gpt-5"] = TokenUsage{TotalTokens: 999}
	if orig.ByModel["gpt-5"].TotalTokens != 100 {
		t.Fatal("Clone mutated original ByModel map")
	}
}
