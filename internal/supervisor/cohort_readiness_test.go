package supervisor

import (
	"context"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
)

func TestProtectedCohortReadinessRefusesCachedArmedMetadataWithoutActor(t *testing.T) {
	s := &Supervisor{m: &host.Manifest{SecretState: &api.SessionSecretState{
		Version: 1, Generation: "generation", Intent: api.SecretDeliveryIntent{Version: 1, TrustedUses: map[string]api.TrustedSecretUseV1{"KEY": {Version: 1, Use: api.TrustedJudgeUseV1}}},
	}}}
	query := &api.TrustedSecretReadinessQueryV1{Version: 1, Generation: "generation", Uses: api.CloneTrustedSecretUses(s.m.SecretState.Intent.TrustedUses)}
	if _, err := s.protectedCohortReadinessV1(context.Background(), query); err == nil {
		t.Fatal("saved metadata substituted for the original private registry actor")
	}
}
