package host

import (
	"context"
	"errors"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestGVisorWalledProbeErrorCodes(t *testing.T) {
	orig := probeGVisorWall
	t.Cleanup(func() { probeGVisorWall = orig })

	cases := []struct {
		name     string
		reason   string
		fix      string
		wantCode string
	}{
		{
			name:     "missing pinned runsc maps to runtime_missing",
			reason:   "pinned runsc release-20261123.1 is not installed in the artefact store",
			fix:      brand.HostBinary + " wall install gvisor",
			wantCode: "runtime_missing",
		},
		{
			name:     "AppArmor restriction maps to wall_setup",
			reason:   "Ubuntu's AppArmor user-namespace restriction blocks runsc (kernel.apparmor_restrict_unprivileged_userns = 1)",
			fix:      "sudo apparmor_parser -r /etc/apparmor.d/" + brand.Name + "-runsc",
			wantCode: "wall_setup",
		},
		{
			name:     "generic smoke failure maps to wall_unavailable",
			reason:   "runsc smoke test failed: exit status 1",
			fix:      "check host doctor",
			wantCode: "wall_unavailable",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			probeGVisorWall = func(context.Context) (bool, string, string, string) {
				return false, "", tc.reason, tc.fix
			}
			_, err := newGVisorWalled(NewArgs{Wall: "gvisor"})
			var pe *proto.Error
			if !errors.As(err, &pe) || pe.Code != tc.wantCode || pe.Hint == "" {
				t.Fatalf("err = %#v, want proto.Error{Code:%q, Hint:...}", err, tc.wantCode)
			}
		})
	}
}
