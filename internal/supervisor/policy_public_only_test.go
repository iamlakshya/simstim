package supervisor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestPublicOnlyFeatureRequiresActualCurrentEgress(t *testing.T) {
	if err := requirePublicOnlyEgress(t.Context(), nil, api.Policy{}); err != nil {
		t.Fatal("legacy policy acquired a capability RPC", err)
	}
	policy := api.Policy{Preset: api.PresetLocked, Rules: []api.Rule{{Layer: api.LayerSession, Allow: true, Target: "api.example.com", PublicOnly: true}}}
	var detail *proto.Error
	if err := requirePublicOnlyEgress(t.Context(), nil, policy); !errors.As(err, &detail) || detail.Code != "helper_old" || detail.Hint == "" {
		t.Fatal("absent owner silently accepted an unsupported restriction")
	}
	fixture := newUnknownAcceptedFixture(t, nil)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := requirePublicOnlyEgress(ctx, fixture.s.eg, policy); err != nil {
		t.Fatal("actual current producer lacks its implemented restriction feature", err)
	}
	if failure := pushPolicyToEgress(fixture.s.eg, "box/session", policy); failure != nil {
		t.Fatal("actual control publication lost the restriction", failure)
	}
}
