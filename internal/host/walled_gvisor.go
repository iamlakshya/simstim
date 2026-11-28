package host

import (
	"context"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/wall/gvisor"
)

// probeGVisorWall checks whether this host can run upstream runsc gVisor walls.
// Tests replace it.
var probeGVisorWall = func(ctx context.Context) (ok bool, ver, reason, fix string) {
	p := gvisor.Driver{}.Probe(ctx)
	return p.OK, p.Version, p.Reason, p.Fix
}

func newGVisorWalled(a NewArgs) (*SessionInfo, error) {
	pctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	ok, runscVer, reason, fix := probeGVisorWall(pctx)
	cancel()
	if !ok {
		code := "wall_unavailable"
		switch {
		case strings.Contains(reason, "not installed in the artefact store"), strings.Contains(reason, "SHA-256 verification"):
			code = "runtime_missing"
		case strings.Contains(reason, "AppArmor"), strings.Contains(reason, "user namespaces"):
			code = "wall_setup"
		}
		if fix == "" {
			fix = brand.HostBinary + " wall install gvisor"
		}
		return nil, UserError(code, reason, fix)
	}
	return newSandboxedWalled(a, "gvisor", gvisor.DriverName, runscVer)
}
