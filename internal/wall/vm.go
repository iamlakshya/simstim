package wall

import (
	"context"
	"slices"

	"github.com/iamlakshya/simstim/internal/vmneed"
)

// VMSpec aliases the one canonical version1 boot Need. It carries no FDs.
type VMSpec = vmneed.BootV1

type expectedSpecKey struct{}

// WithExpectedSpec carries a freshly validated canonical trusted Need plan.
// The host obtains it from the authenticated client or its protected trusted
// manifest recording that Need, including automatic restore. VM-writable
// layout, boot evidence and cache claims cannot supply it. No FD is serialised.
func WithExpectedSpec(ctx context.Context, spec Spec) context.Context {
	return context.WithValue(ctx, expectedSpecKey{}, cloneExpectedSpec(spec))
}

// ExpectedSpec returns a copy of that trusted plan. A driver's saved VM-data
// layout cannot create execution authority after a restart.
func ExpectedSpec(ctx context.Context) (Spec, bool) {
	spec, ok := ctx.Value(expectedSpecKey{}).(Spec)
	return cloneExpectedSpec(spec), ok
}

func cloneExpectedSpec(spec Spec) Spec {
	spec.Mounts = slices.Clone(spec.Mounts)
	spec.StateDirs = slices.Clone(spec.StateDirs)
	spec.StateFiles = slices.Clone(spec.StateFiles)
	if spec.VM != nil {
		vm := spec.VM.Clone()
		spec.VM = &vm
	}
	return spec
}
