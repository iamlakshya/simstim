package host

import (
	"context"
	"runtime"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/wall"
	"github.com/iamlakshya/simstim/internal/wall/process"
)

const bwrapFixID = "bubblewrap"

// Test hooks for the bubblewrap manual root-step fix.
var (
	bwrapFixGOOS = runtime.GOOS
	bwrapProbeFn = func(ctx context.Context) wall.Probe { return process.Driver{}.Probe(ctx) }
)

// bwrapFix returns a manual one-time root-step fix for host doctor --fix when
// a Linux host needs bubblewrap, Ubuntu's bubblewrap AppArmor profile, or
// unprivileged user namespaces enabled (spec H7, decision 0053). Nothing here
// runs sudo.
func bwrapFix() []fixItem {
	if bwrapFixGOOS != "linux" {
		return nil
	}
	p := bwrapProbeFn(context.Background())
	if p.OK || !strings.HasPrefix(p.Fix, "sudo ") {
		return nil
	}
	return []fixItem{{Fix: api.Fix{
		ID:      bwrapFixID,
		Problem: p.Reason,
		Action:  bwrapFixAction(p),
		Command: p.Fix,
		Manual:  true,
	}}}
}

func bwrapFixAction(p wall.Probe) string {
	switch {
	case strings.Contains(p.Fix, "bwrap-userns-restrict") && strings.Contains(p.Fix, "bubblewrap"):
		return "install bubblewrap and load the distribution's bubblewrap AppArmor profile as root (one-time step)"
	case strings.Contains(p.Fix, "apparmor_parser"):
		return "install and load the distribution's bubblewrap AppArmor profile as root (one-time step)"
	case strings.Contains(p.Fix, "sysctl"):
		return "enable unprivileged user namespaces with sysctl as root (one-time step)"
	default:
		return "install the distribution's bubblewrap package as root (one-time step)"
	}
}
