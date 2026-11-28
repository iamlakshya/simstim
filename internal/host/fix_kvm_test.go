package host

import (
	"context"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/wall"
)

func stubKVMFixProbe(t *testing.T, goos string, p wall.Probe) {
	t.Helper()
	origOS := kvmFixGOOS
	origFn := kvmProbeFn
	t.Cleanup(func() {
		kvmFixGOOS = origOS
		kvmProbeFn = origFn
	})
	kvmFixGOOS = goos
	kvmProbeFn = func(context.Context) wall.Probe { return p }
}

func TestKVMFixManualRootSteps(t *testing.T) {
	t.Run("non-linux returns none", func(t *testing.T) {
		stubKVMFixProbe(t, "darwin", wall.Probe{
			OK:    false,
			Fix:   "sudo usermod -aG kvm admin",
			Facts: map[string]string{"kvm": "present", "kvm_usable": "false"},
		})
		if fixes := kvmFix(); len(fixes) != 0 {
			t.Fatalf("darwin kvmFix = %+v, want none", fixes)
		}
	})

	t.Run("ready or absent kvm returns none", func(t *testing.T) {
		stubKVMFixProbe(t, "linux", wall.Probe{OK: true, Facts: map[string]string{"kvm": "present", "kvm_usable": "true"}})
		if fixes := kvmFix(); len(fixes) != 0 {
			t.Fatalf("ready kvmFix = %+v, want none", fixes)
		}
		stubKVMFixProbe(t, "linux", wall.Probe{OK: false, Fix: "enable KVM", Facts: map[string]string{"kvm": "absent"}})
		if fixes := kvmFix(); len(fixes) != 0 {
			t.Fatalf("absent kvmFix = %+v, want none", fixes)
		}
	})

	t.Run("kvm group only (no setfacl, e.g. Debian 13 cloud image)", func(t *testing.T) {
		stubKVMFixProbe(t, "linux", wall.Probe{
			OK:     false,
			Reason: "/dev/kvm is not accessible to admin (open /dev/kvm: permission denied)",
			Fix:    "sudo usermod -aG kvm admin",
			Facts:  map[string]string{"kvm": "present", "kvm_usable": "false"},
		})
		fixes := kvmFix()
		if len(fixes) != 1 {
			t.Fatalf("kvmFix = %+v, want 1 item", fixes)
		}
		f := fixes[0]
		if f.ID != kvmFixID || !f.Manual || f.Command != "sudo usermod -aG kvm admin" {
			t.Fatalf("fix = %+v", f)
		}
		if !strings.Contains(f.Action, "kvm group") {
			t.Errorf("action = %q, want kvm group description", f.Action)
		}
	})

	t.Run("kvm setfacl plus group", func(t *testing.T) {
		cmd := `sudo sh -c 'setfacl -m "u:$1:rw" /dev/kvm && usermod -aG kvm "$1"' _ alice`
		stubKVMFixProbe(t, "linux", wall.Probe{
			OK:     false,
			Reason: "/dev/kvm is not accessible to alice (permission denied)",
			Fix:    cmd,
			Facts:  map[string]string{"kvm": "present", "kvm_usable": "false"},
		})
		fixes := kvmFix()
		if len(fixes) != 1 || !fixes[0].Manual || fixes[0].Command != cmd || !strings.Contains(fixes[0].Action, "setfacl") {
			t.Fatalf("kvmFix setfacl = %+v", fixes)
		}
	})
}

func TestKVMManualFixNeverAutoRuns(t *testing.T) {
	secretHome(t)
	stubKVMFixProbe(t, "linux", wall.Probe{
		OK:     false,
		Reason: "/dev/kvm is not accessible to admin (permission denied)",
		Fix:    "sudo usermod -aG kvm admin",
		Facts:  map[string]string{"kvm": "present", "kvm_usable": "false"},
	})
	res := Fixes(api.FixArgs{Apply: true, IDs: []string{kvmFixID}})
	f, ok := fixByID(res, kvmFixID)
	if !ok || !f.Manual || f.Done || f.Error != "" {
		t.Fatalf("manual kvm fix after Apply=true: %+v (found=%v)", f, ok)
	}
}
