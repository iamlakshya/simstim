package supervisor

import (
	"context"
	"reflect"
	"runtime"

	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/wall"
	"github.com/iamlakshya/simstim/internal/wall/gvisor"
)

// originalGvisorPlan checks the SAME accepted outside boot/OP before any
// filesystem preparation. Manifest and ExpectedSpec remain data projections;
// neither can issue the original native receipt or its storage domain.
func originalGvisorPlan(ctx context.Context, m *host.Manifest) (wall.Spec, error) {
	if ctx == nil {
		return wall.Spec{}, execRefusal("gvisor preparation has no native owner lifetime")
	}
	if err := ctx.Err(); err != nil {
		return wall.Spec{}, err
	}
	if host.OriginalVMNeedFromContext(ctx) == nil {
		return wall.Spec{}, execRefusal("gvisor preparation has no original accepted native operation")
	}
	if m == nil || m.Wall == nil || m.Wall.Level != "gvisor" || m.Wall.Driver != "gvisor" || m.SandboxOf != "" || len(m.VMNeedV1) != 0 || m.RuntimeManifest == nil {
		return wall.Spec{}, execRefusal("the gvisor primary conflicts with its original runtime selection")
	}
	expected, known := wall.ExpectedSpec(ctx)
	if !known || expected.ID != m.ID || expected.SessionDir != paths.SessionDir(m.ID) || expected.MemoryMax != m.Limits.MemoryMax || expected.Image != nil || expected.VM != nil || expected.RuntimeManifest == nil {
		return wall.Spec{}, execRefusal("the gvisor filesystem plan has no matching original boot projection")
	}
	selected, err := host.ExpectedOriginalRuntime(ctx)
	if err != nil {
		return wall.Spec{}, err
	}
	if !reflect.DeepEqual(selected, *m.RuntimeManifest) || !reflect.DeepEqual(selected, *expected.RuntimeManifest) {
		return wall.Spec{}, execRefusal("the gvisor runtime differs from its original accepted selection")
	}
	if _, err := gvisor.RuntimeItemsV1(selected, runtime.GOARCH); err != nil {
		return wall.Spec{}, err
	}
	if _, err := host.OriginalBootDomain(ctx); err != nil {
		return wall.Spec{}, err
	}
	return expected, nil
}
