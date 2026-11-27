package wall

import (
	"context"
	"slices"

	"github.com/iamlakshya/simstim/internal/vmneed"
)

// VMSpec aliases the one canonical version1 boot Need. It carries no FDs.
type VMSpec = vmneed.BootV1

type expectedSpecKey struct{}

// WithExpectedSpec carries an independent plan-data projection. It creates no
// operation, native boot owner or execution authority. Actual runtime consumers
// still require their original authenticated host receipt and native binding.
func WithExpectedSpec(ctx context.Context, spec Spec) context.Context {
	return context.WithValue(ctx, expectedSpecKey{}, cloneExpectedSpec(spec))
}

// ExpectedSpec returns independent plan data. A copied context, saved layout
// or caller-provided spec cannot issue the original native host boot receipt.
func ExpectedSpec(ctx context.Context) (Spec, bool) {
	spec, ok := ctx.Value(expectedSpecKey{}).(Spec)
	return cloneExpectedSpec(spec), ok
}

func cloneExpectedSpec(spec Spec) Spec {
	spec.Mounts = slices.Clone(spec.Mounts)
	spec.StateDirs = slices.Clone(spec.StateDirs)
	spec.StateFiles = slices.Clone(spec.StateFiles)
	if spec.RuntimeManifest != nil {
		manifest := *spec.RuntimeManifest
		manifest.Items = slices.Clone(manifest.Items)
		manifest.Advisories = slices.Clone(manifest.Advisories)
		spec.RuntimeManifest = &manifest
	}
	if spec.VM != nil {
		vm := spec.VM.Clone()
		spec.VM = &vm
	}
	return spec
}
