package host

import (
	"context"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/wall"
)

func stubBwrapFixProbe(t *testing.T, goos string, p wall.Probe) {
	t.Helper()
	origOS := bwrapFixGOOS
	origFn := bwrapProbeFn
	t.Cleanup(func() {
		bwrapFixGOOS = origOS
		bwrapProbeFn = origFn
	})
	bwrapFixGOOS = goos
	bwrapProbeFn = func(context.Context) wall.Probe { return p }
}

func TestBwrapFixNonLinuxAndReady(t *testing.T) {
	stubBwrapFixProbe(t, "darwin", wall.Probe{OK: false, Reason: "missing", Fix: "sudo apt install bubblewrap"})
	if fixes := bwrapFix(); len(fixes) != 0 {
		t.Fatalf("darwin fixes = %+v, want none", fixes)
	}

	stubBwrapFixProbe(t, "linux", wall.Probe{OK: true})
	if fixes := bwrapFix(); len(fixes) != 0 {
		t.Fatalf("ready fixes = %+v, want none", fixes)
	}

	// Non-sudo probe failures (such as old kernel or missing Landlock) are not
	// root-step commands for host doctor --fix.
	stubBwrapFixProbe(t, "linux", wall.Probe{
		OK:     false,
		Reason: "Landlock is not enabled on this kernel",
		Fix:    "boot a kernel with Landlock enabled",
	})
	if fixes := bwrapFix(); len(fixes) != 0 {
		t.Fatalf("non-sudo fixes = %+v, want none", fixes)
	}
}

func TestBwrapFixManualRootSteps(t *testing.T) {
	cases := []struct {
		name          string
		probe         wall.Probe
		wantActionHas string
	}{
		{
			name: "missing bwrap on debian",
			probe: wall.Probe{
				OK:     false,
				Reason: "bubblewrap (bwrap) is not installed",
				Fix:    "sudo sh -c 'apt-get update && apt-get install -y bubblewrap'",
			},
			wantActionHas: "install the distribution's bubblewrap package as root",
		},
		{
			name: "ubuntu 24.04 missing bwrap and apparmor profile",
			probe: wall.Probe{
				OK:     false,
				Reason: "bubblewrap (bwrap) is not installed",
				Fix:    "sudo sh -c 'apt-get update && apt-get install -y bubblewrap apparmor-profiles apparmor-utils && install -m 0644 /usr/share/apparmor/extra-profiles/bwrap-userns-restrict /etc/apparmor.d/bwrap-userns-restrict && apparmor_parser -r /etc/apparmor.d/bwrap-userns-restrict'",
			},
			wantActionHas: "install bubblewrap and load the distribution's bubblewrap AppArmor profile",
		},
		{
			name: "ubuntu 24.04 apparmor blocks installed bwrap",
			probe: wall.Probe{
				OK:     false,
				Reason: "Ubuntu's AppArmor user-namespace restriction blocks bubblewrap (kernel.apparmor_restrict_unprivileged_userns = 1)",
				Fix:    "sudo apparmor_parser -r /etc/apparmor.d/bwrap-userns-restrict",
			},
			wantActionHas: "AppArmor profile as root",
		},
		{
			name: "sysctl user namespaces disabled",
			probe: wall.Probe{
				OK:     false,
				Reason: "user namespaces are disabled (user.max_user_namespaces = 0)",
				Fix:    "sudo sh -c 'printf \"user.max_user_namespaces = 15000\\n\" > /etc/sysctl.d/50-test-userns.conf && sysctl -p /etc/sysctl.d/50-test-userns.conf'",
			},
			wantActionHas: "enable unprivileged user namespaces with sysctl as root",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubBwrapFixProbe(t, "linux", tc.probe)
			fixes := bwrapFix()
			if len(fixes) != 1 {
				t.Fatalf("bwrapFix = %+v, want 1 item", fixes)
			}
			f := fixes[0]
			if f.ID != bwrapFixID || !f.Manual || f.Command != tc.probe.Fix || f.Problem != tc.probe.Reason {
				t.Errorf("fix = %+v", f)
			}
			if !strings.Contains(f.Action, tc.wantActionHas) {
				t.Errorf("action = %q, want substring %q", f.Action, tc.wantActionHas)
			}
			if strings.Count(f.Command, "sudo") != 1 {
				t.Errorf("command should use one sudo: %q", f.Command)
			}
		})
	}
}

func TestBwrapManualFixNeverAutoRuns(t *testing.T) {
	secretHome(t)
	stubBwrapFixProbe(t, "linux", wall.Probe{
		OK:     false,
		Reason: "bubblewrap (bwrap) is not installed",
		Fix:    "sudo sh -c 'apt-get update && apt-get install -y bubblewrap apparmor-profiles apparmor-utils && install -m 0644 /usr/share/apparmor/extra-profiles/bwrap-userns-restrict /etc/apparmor.d/bwrap-userns-restrict && apparmor_parser -r /etc/apparmor.d/bwrap-userns-restrict'",
	})
	res := Fixes(api.FixArgs{Apply: true, IDs: []string{bwrapFixID}})
	f, ok := fixByID(res, bwrapFixID)
	if !ok || !f.Manual || f.Done || f.Error != "" {
		t.Fatalf("manual bwrap fix after Apply=true: %+v (found=%v)", f, ok)
	}
}
