package egress

import (
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestClassifierPreinitCheckSharesPureAdapterRules(t *testing.T) {
	policy := api.Policy{LayerValues: api.LayerValues{Classifier: judgeSettings()}}
	if err := CheckClassifierConfigurationV1(policy); err != nil {
		t.Fatal("configured pure model metadata was rejected", err)
	}
	for _, change := range []func(*api.Policy){
		func(p *api.Policy) { p.Classifier.Model = nil },
		func(p *api.Policy) { value := "wrong"; p.Classifier.MaxScope = &value },
		func(p *api.Policy) { value := "invalid"; p.Classifier.Timeout = &value },
		func(p *api.Policy) {
			value := "http://model.example/v1/chat/completions"
			p.Classifier.Endpoint = &value
		},
		func(p *api.Policy) {
			p.Origins = []api.ValueOrigin{{Key: "classifier.model", Source: api.SourceRepositoryFile}}
		},
	} {
		copy := api.ClonePolicy(policy)
		change(&copy)
		if err := CheckClassifierConfigurationV1(copy); err == nil {
			t.Fatal("invalid or untrusted adapter passed optional-I/O preflight")
		}
	}
}
