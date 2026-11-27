package client

import (
	"encoding/json"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestHeldRepositoryKeyCannotBroadenAnOrganisationSelection(t *testing.T) {
	for _, exact := range []bool{false, true} {
		t.Run(map[bool]string{false: "organisation-only", true: "exact-repository"}[exact], func(t *testing.T) {
			c, _, args, _ := positiveHoldFixtureV1(t, api.NetworkHoldOnce, "")
			layers := []api.Layer{{Kind: api.LayerAll}, {Kind: api.LayerHost, Name: c.Host.Name}, {Kind: api.LayerRepo, Name: "forge.example/team/*"}}
			if exact {
				layers = append(layers, api.Layer{Kind: api.LayerRepo, Name: "forge.example/team/app"})
			}
			layers = append(layers, api.Layer{Kind: api.LayerSession})
			pin, err := api.PinPolicySources(layers)
			if err != nil {
				t.Fatal(err)
			}
			creation := h5Policy()
			creation.SessionID, creation.Ceiling, creation.CreationWall, creation.Secrets = args.Session, TierOwn, "process", nil
			creation.PolicySources, err = json.Marshal(pin)
			if err != nil {
				t.Fatal(err)
			}
			if err := reserveSandbox(t.Context(), c.Host, creation); err != nil {
				t.Fatal(err)
			}
			selected, err := c.NetworkHoldFileLayerV1(t.Context(), args.Session, api.NetworkHoldRepo)
			if !exact {
				if err == nil || selected != (FileLayer{}) {
					t.Fatal("one repository key widened an answer to its organisation", selected, err)
				}
			} else if err != nil || selected.Kind != api.LayerRepo || selected.Name != "forge.example/team/app" {
				t.Fatal("actual original exact repository was not retained", selected, err)
			}
		})
	}
}
