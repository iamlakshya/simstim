package api

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func modelUsageWireFixture() ModelUsageV1 {
	counts := ModelUsageCountsV1{Requests: 2, EstimatedRequests: 1, TotalTokens: 9007199254740993}
	return ModelUsageV1{Version: 1, Scope: "sandbox", Total: counts, Unattributed: counts}
}

func TestModelUsageWireClosesFieldsAndPreservesLargeCounters(t *testing.T) {
	want := modelUsageWireFixture()
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got ModelUsageV1
	if err := json.Unmarshal(data, &got); err != nil || got.Total.TotalTokens != want.Total.TotalTokens || !ValidModelUsageV1(got) {
		t.Fatalf("valid accounting wire changed: %+v %v", got, err)
	}
	for _, raw := range []string{
		strings.Replace(string(data), `"v":1`, `"v":2`, 1),
		strings.Replace(string(data), `"scope":`, `"Scope":`, 1),
		strings.Replace(string(data), `"v":1`, `"v":1,"v":1`, 1),
		strings.Replace(string(data), `{`, `{"future":true,`, 1),
		strings.Replace(string(data), `"total_tokens":9007199254740993`, `"total_tokens":-1`, 1),
	} {
		if json.Unmarshal([]byte(raw), &got) == nil {
			t.Fatalf("unsupported accounting metadata was accepted: %s", raw)
		}
	}
}

func TestModelUsageSnapshotRequiresExactBreakdownAndOverflowSum(t *testing.T) {
	model := ModelUsageCountsV1{Requests: 1, TotalTokens: 5}
	overflow := ModelUsageCountsV1{Requests: 2, EstimatedRequests: 1, TotalTokens: 7}
	total, _ := AddModelUsageCountsV1(model, overflow)
	snapshot := ModelUsageV1{Version: 1, Scope: "sandbox", Total: total, Models: []ModelUsageModelV1{{Protocol: "openai-responses", Model: "model-a", Counts: model}}, Unattributed: overflow}
	if !ValidModelUsageV1(snapshot) {
		t.Fatal("valid exact breakdown refused")
	}
	copy := CloneModelUsageV1(&snapshot)
	copy.Models[0].Counts.TotalTokens++
	if ValidModelUsageV1(*copy) || snapshot.Models[0].Counts.TotalTokens != 5 {
		t.Fatal("bad sum or borrowed model memory became valid")
	}
}

func TestModelUsageCheckedCounterAdditionMarksSaturation(t *testing.T) {
	counts, overflow := AddModelUsageCountsV1(ModelUsageCountsV1{Requests: math.MaxUint64, TotalTokens: math.MaxUint64}, ModelUsageCountsV1{Requests: 1, TotalTokens: 2})
	if !overflow || counts.Requests != math.MaxUint64 || counts.TotalTokens != math.MaxUint64 {
		t.Fatalf("counter wrapped: %+v %v", counts, overflow)
	}
}
