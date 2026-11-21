package client

import (
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
)

// This is the same pure pinned-floor/config selection as actual preparation.
// The metadata minimum may reject that choice, never silently upgrade it to a
// stronger capability probe. The original args remain unchanged for the R.
func chooseAutoPlacementWallV1(host Host, tier Tier, args NewArgs, layers []api.Layer, need PlacementNeed) (WallChoice, string, error) {
	policy, _, err := egress.MergeLayers(layers)
	if err != nil {
		return WallChoice{}, "", policyError(err)
	}
	request := args.Wall
	namedMinimum := ""
	if request != "" && request != "auto" {
		namedMinimum = request
	}
	if namedMinimum == "" && policy.Sandbox.Wall != nil {
		request = *policy.Sandbox.Wall
		if request != "auto" && request != "" {
			namedMinimum = request
		}
		if !args.Downgrade && !sessionHasWall(layers) {
			request = raisedConfigWall(request, host.WallFloor)
		}
	}
	choice, err := ChooseWall(host, tier, request, args.Downgrade)
	if err != nil {
		return choice, "", err
	}
	minimum := need.MinLevel
	if minimum == "" {
		minimum = "microvm"
		if namedMinimum != "" {
			minimum = namedMinimum
		}
	}
	if minimum != "microvm" && minimum != "gvisor" {
		return choice, minimum, placementNeedError()
	}
	return choice, minimum, nil
}
