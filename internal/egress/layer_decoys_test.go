package egress

import (
	"errors"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func decoyTestValue(value bool) api.LayerValues {
	return api.LayerValues{Sandbox: api.SandboxValues{Decoys: &value}}
}

func TestDecoyFalseDominatesOnlyAfterOrdinaryLocksAcceptTheLayers(t *testing.T) {
	for _, test := range []struct {
		name   string
		layers []api.Layer
		locked bool
		origin api.LayerKind
	}{
		{"earlier false later true", []api.Layer{{Kind: api.LayerAll, LayerValues: decoyTestValue(false)}, {Kind: api.LayerSession, LayerValues: decoyTestValue(true)}}, false, api.LayerAll},
		{"earlier true later false", []api.Layer{{Kind: api.LayerAll, LayerValues: decoyTestValue(true)}, {Kind: api.LayerSession, LayerValues: decoyTestValue(false)}}, false, api.LayerSession},
		{"true lock false setting", []api.Layer{{Kind: api.LayerAll, Lock: &api.Lock{LayerValues: decoyTestValue(true)}}, {Kind: api.LayerSession, LayerValues: decoyTestValue(false)}}, true, ""},
		{"false lock true setting", []api.Layer{{Kind: api.LayerAll, Lock: &api.Lock{LayerValues: decoyTestValue(false)}}, {Kind: api.LayerSession, LayerValues: decoyTestValue(true)}}, true, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			policy, layers, err := MergeLayers(test.layers)
			if test.locked {
				var detail *proto.Error
				if !errors.As(err, &detail) || detail.Code != "policy_locked" || detail.Hint == "" || layers != nil || policy.Preset != "" {
					t.Fatal("locked refusal published a partial policy or lost its hint", err)
				}
				return
			}
			if err != nil || policy.Sandbox.Decoys == nil || *policy.Sandbox.Decoys {
				t.Fatal("a valid opt-out was revived", err)
			}
			found := false
			for _, origin := range policy.Origins {
				if origin.Key == "sandbox.decoys" {
					found = origin.Layer == test.origin
				}
			}
			if !found {
				t.Fatal("resolved decoy floor lost its disabling source")
			}
			if len(layers) != 2 {
				t.Fatal("merge lost original source layers")
			}
		})
	}
}

func TestDecoyFalseCannotBeRevivedByALaterTrueFixedLock(t *testing.T) {
	layers := []api.Layer{{Kind: api.LayerAll, LayerValues: decoyTestValue(false)}, {Kind: api.LayerHost, Lock: &api.Lock{LayerValues: decoyTestValue(true)}}}
	policy, cleaned, err := MergeLayers(layers)
	var detail *proto.Error
	if !errors.As(err, &detail) || detail.Code != "policy_locked" || cleaned != nil || policy.Sandbox.Decoys != nil {
		t.Fatal("a fixed lock silently revived an earlier opt-out", err)
	}
}
