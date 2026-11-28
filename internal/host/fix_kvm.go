package host

import (
	"context"
	"runtime"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/wall"
	"github.com/iamlakshya/simstim/internal/wall/chv"
)

const kvmFixID = "kvm-access"

// Test hooks for the KVM manual root-step fix.
var (
	kvmFixGOOS = runtime.GOOS
	kvmProbeFn = func(ctx context.Context) wall.Probe { return chv.Driver{}.Probe(ctx) }
)

// kvmFix returns a manual one-time root-step fix for host doctor --fix when
// /dev/kvm is present on a Linux host but not accessible to the user
// (isolation spec 14.4.2, P3.7). Nothing here runs sudo.
func kvmFix() []fixItem {
	if kvmFixGOOS != "linux" {
		return nil
	}
	p := kvmProbeFn(context.Background())
	if p.OK || p.Facts["kvm"] != "present" || p.Facts["kvm_usable"] != "false" || !strings.HasPrefix(p.Fix, "sudo ") {
		return nil
	}
	return []fixItem{{Fix: api.Fix{
		ID:      kvmFixID,
		Problem: p.Reason,
		Action:  kvmFixAction(p),
		Command: p.Fix,
		Manual:  true,
	}}}
}

func kvmFixAction(p wall.Probe) string {
	if strings.Contains(p.Fix, "setfacl") {
		return "grant /dev/kvm access now with setfacl and add your user to the kvm group for future boots (one-time step)"
	}
	return "add your user to the kvm group as root, then reboot or restart your login session (one-time step)"
}
