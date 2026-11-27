package egress

import (
	"github.com/iamlakshya/simstim/internal/api"
	"testing"
)

func TestPublicOnlyLayerCannotRewriteAnOrdinarySuppliedIdentity(t *testing.T) {
	row := api.Rule{Layer: api.LayerSession, Allow: true, Target: "api.example.com"}
	row.ID = api.RuleID(row)
	row.PublicOnly = true
	if _, _, err := MergeLayers([]api.Layer{{Kind: api.LayerSession, Rules: []api.Rule{row}}}); err == nil {
		t.Fatal("layer cleanup silently rewrote an ordinary identity for a restricted rule")
	}
	row.ID = ""
	policy, _, err := MergeLayers([]api.Layer{{Kind: api.LayerSession, Rules: []api.Rule{row}}})
	if err != nil || len(policy.Rules) != 1 || !policy.Rules[0].PublicOnly || policy.Rules[0].ID != api.RuleID(row) {
		t.Fatal("fresh restricted layer identity was refused or changed", err)
	}
}
