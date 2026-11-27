package supervisor

import (
	"context"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
)

func TestNoneExposesOnlyActualApprovedValuesWithoutEgress(t *testing.T) {
	m := &host.Manifest{ID: "a2a2a2a2", Wall: &host.WallRecord{Level: "none", Driver: "none"}, Secrets: []string{"TOKEN"}, SecretState: &api.SessionSecretState{Version: 1, Intent: api.SecretDeliveryIntent{Version: 1, Modes: map[string]api.SecretMode{"TOKEN": api.SecretModeExposed}}}}
	s := &Supervisor{m: m}
	environment, err := s.startNoneEnvironmentV1(context.Background(), map[string]string{"TOKEN": "actual"})
	if err != nil {
		t.Fatal(err)
	}
	if environment.Env["TOKEN"] != "actual" || environment.Modes != nil || s.eg != nil || s.egressStarted || s.bootDecoyFiles != nil {
		t.Fatal("none started an egress or sentinel path")
	}
	if !s.memberReady || s.m.SecretState.Primary["TOKEN"].Sentinel != "" || s.m.SecretState.Primary["TOKEN"].Mode != api.SecretModeExposed {
		t.Fatal("none lost its applied exposure metadata")
	}
	if _, err := s.noneEnvironmentV1(m, map[string]string{"OTHER": "foreign"}); err == nil {
		t.Fatal("none accepted a value outside its original names")
	}
	bound := host.CloneManifest(*m)
	bound.SecretState.Intent.Modes["TOKEN"] = api.SecretModeBound
	if _, err := s.noneEnvironmentV1(&bound, map[string]string{"TOKEN": "actual"}); err == nil {
		t.Fatal("none silently downgraded a bound secret to exposure")
	}
}
