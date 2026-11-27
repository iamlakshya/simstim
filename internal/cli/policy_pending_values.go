package cli

import (
	"fmt"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/egress"
)

func printPendingPolicyValues(policy api.Policy) {
	if len(policy.NextStart) == 0 {
		return
	}
	keys := append([]string{}, policy.NextStart...)
	desired, err := api.ReplacePolicyLaptopLayersV1(policy.Sources, policy.NextLayers)
	if err == nil {
		merged, _, mergeErr := egress.MergeLayers(desired)
		if mergeErr == nil && merged.Sandbox.Wall != nil {
			for at, key := range keys {
				if key == "sandbox.wall" {
					keys[at] += "=" + *merged.Sandbox.Wall
				}
			}
		}
	}
	fmt.Printf("At the next start: %s\n", client.CleanLine(strings.Join(keys, ", ")))
}
