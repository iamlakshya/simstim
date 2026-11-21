package client

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
)

var updateRootGoldens = flag.Bool("update-root", false, "rewrite the reviewed root script and undo fixtures")

var initialRootStepIDs = []RootStepID{
	"kvm-acl", "kvm-group", "linger", "delegate-io", "subuid", "io-uring-off", "vsock-modules",
}

// rootTestState isolates every location that a root-step test can read or write.
func rootTestState(t testing.TB) {
	t.Helper()
	dir := t.TempDir()
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR", "XDG_CONFIG_DIRS", "XDG_DATA_DIRS", "GNUPGHOME"} {
		path := filepath.Join(dir, key)
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv(key, path)
	}
	// Keep SSH's socket path short enough that it never falls back to a
	// persistent per-user folder in /tmp.
	if runtime.GOOS != "windows" {
		cache, err := os.MkdirTemp("/tmp", "rt-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(cache) })
		canonical, err := filepath.EvalSymlinks(cache)
		if err != nil {
			t.Fatal(err)
		}
		t.Setenv("XDG_CACHE_HOME", canonical)
	}
	for _, key := range []string{"GIT_CONFIG", "GIT_CONFIG_GLOBAL"} {
		t.Setenv(key, filepath.Join(dir, "gitconfig"))
	}
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_COUNT", "0")
}

func assertRootError(t *testing.T, err error) {
	t.Helper()
	var ue *UserError
	if !errors.As(err, &ue) || ue.Msg == "" || ue.Code == "" || ue.Hint == "" {
		t.Fatalf("root step error has no message, code or hint: %#v", err)
	}
}

func TestBuildRootScriptRejectsUnsafeIdentity(t *testing.T) {
	rootTestState(t)
	for _, name := range []string{"", "root", "Root", "0user", "-user", "user.name", "user/name", "user\\name", "a b", "a\nb", "a\r", "a'", "a\"", "a;id", "$USER", "$(id)", "`id`", "élise", "a\x00b", strings.Repeat("a", 33)} {
		t.Run(fmt.Sprintf("%q", name), func(t *testing.T) {
			rs, err := BuildRootScript(name, 1000, []RootStepID{RootLinger}, 0)
			assertRootError(t, err)
			if !reflect.DeepEqual(rs, RootScript{}) {
				t.Fatal("a refused identity returned a partial script")
			}
		})
	}
	for _, uid := range []int{-1, 0} {
		_, err := BuildRootScript("me", uid, []RootStepID{RootLinger}, 0)
		assertRootError(t, err)
	}
	for _, name := range []string{"a", "_", "_svc", "a_b-c012", strings.Repeat("a", 32)} {
		if _, err := BuildRootScript(name, 1000, []RootStepID{RootLinger}, 0); err != nil {
			t.Errorf("valid account %q: %v", name, err)
		}
	}
}

func TestBuildRootScriptRejectsUnsupportedSteps(t *testing.T) {
	rootTestState(t)
	for _, id := range []RootStepID{"", "KVM-ACL", "kvm-acl;id", "userns-apparmor", "launcher", "slots", "future"} {
		rs, err := BuildRootScript("me", 1000, []RootStepID{RootLinger, id}, 0)
		assertRootError(t, err)
		if !reflect.DeepEqual(rs, RootScript{}) {
			t.Fatalf("unknown step %q returned a partial script", id)
		}
	}
	_, err := BuildRootScript("me", 1000, []RootStepID{RootLinger, RootLinger}, 0)
	assertRootError(t, err)
	for _, slots := range []int{-1, 1, 16} {
		_, err := BuildRootScript("me", 1000, []RootStepID{RootLinger}, slots)
		assertRootError(t, err)
	}
}

func TestBuildRootScriptExplainsEveryChange(t *testing.T) {
	rootTestState(t)
	ids := slices.Clone(initialRootStepIDs)
	rs, err := BuildRootScript("me", 1000, ids, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs.Steps) != len(initialRootStepIDs) {
		t.Fatalf("got %d step explanations", len(rs.Steps))
	}
	for i, step := range rs.Steps {
		if step.ID != initialRootStepIDs[i] || step.Line == "" || step.Reason == "" || step.Undo == "" {
			t.Errorf("step %d has an incomplete explanation: %+v", i, step)
		}
		machine := step.ID == RootIOUringOff || step.ID == RootVsockModules
		if step.MachineWide != machine {
			t.Errorf("step %s machine-wide = %v, want %v", step.ID, step.MachineWide, machine)
		}
	}
	for _, text := range []string{rs.Text, rs.Undo} {
		if strings.Contains(text, "$USER") || strings.Contains(text, "${USER}") || strings.Contains(text, "@USER@") || strings.Contains(text, "@UID@") || strings.Contains(text, "@BRAND@") {
			t.Fatal("script uses sudo's user or an unresolved template value")
		}
		for _, forbidden := range []string{"sudo ", "apt install", "curl ", "wget "} {
			if strings.Contains(text, forbidden) {
				t.Errorf("root script invokes unapproved command %q", forbidden)
			}
		}
	}
	undoInstalled := strings.Index(rs.Text, "mv -f \"$d/undo.sh.new\" \"$d/undo.sh\"")
	firstStep := strings.Index(rs.Text, "if begin kvm-acl;")
	if undoInstalled < 0 || firstStep < undoInstalled {
		t.Fatal("the undo is not installed before the first privileged step")
	}
	sum := sha256.Sum256([]byte(rs.Text))
	if rs.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatal("displayed SHA-256 does not name the script's exact bytes")
	}
	before := rs.Text
	ids[0] = RootLinger
	if rs.Text != before || rs.Steps[0].ID != RootKVMACL {
		t.Fatal("changing the caller's step list changed the built script")
	}
}

// Every subset has separate script and undo fixtures. Updating fixtures is an
// explicit operation. A missing fixture fails, so an ungenerated set is visible.
func TestRootScriptGoldens(t *testing.T) {
	rootTestState(t)
	for mix := 0; mix < 1<<len(initialRootStepIDs); mix++ {
		t.Run(fmt.Sprintf("%02x", mix), func(t *testing.T) {
			var ids []RootStepID
			for bit, id := range initialRootStepIDs {
				if mix&(1<<bit) != 0 {
					ids = append(ids, id)
				}
			}
			rs, err := BuildRootScript("me", 1001, ids, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, fixture := range []struct{ suffix, text string }{{"script", rs.Text}, {"undo", rs.Undo}} {
				name := filepath.Join("testdata", "root", fmt.Sprintf("%02x.%s.golden", mix, fixture.suffix))
				if *updateRootGoldens {
					if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(name, []byte(strings.ReplaceAll(fixture.text, brand.Name, "@BRAND@")), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				want, err := os.ReadFile(name)
				if err != nil {
					t.Fatalf("read root fixture: %v (generate with -update-root, then review every fixture)", err)
				}
				if fixture.text != strings.ReplaceAll(string(want), "@BRAND@", brand.Name) {
					t.Errorf("%s changed: review the root script and its undo before updating the fixture", name)
				}
			}
		})
	}
}

func rootSelectionProbe() api.CapProbe {
	return api.CapProbe{OS: "linux", Arch: "amd64", Kernel: "6.12.1", User: "me", UID: 1000,
		Linger: true, Delegated: []string{"cpu", "memory", "pids", "io"}, Subuid: true,
		IOURingDisabled: 2, VsockBlocked: true, UserNS: true, Seccomp: true, LandlockABI: 6,
		Walls: []api.WallProbe{{Driver: "process", Level: "process", OK: true}}}
}

func rootSelectionRuntime() api.RuntimeStatus {
	return api.RuntimeStatus{Drivers: []api.RuntimeDriver{{Name: "chv", Arch: "amd64", KernelMin: "6.8", Verified: true}}}
}

func TestRootStepsForNeedsAndPrerequisites(t *testing.T) {
	rootTestState(t)
	micro := []api.WallProbe{{Driver: "chv", Level: "microvm", Facts: map[string]string{"root_blocker": "kvm_access"}}}
	for _, tt := range []struct {
		name     string
		change   func(*api.CapProbe, *api.RuntimeStatus)
		delegate bool
		want     []RootStepID
	}{
		{"nothing missing", func(*api.CapProbe, *api.RuntimeStatus) {}, false, nil},
		{"KVM driver access", func(p *api.CapProbe, _ *api.RuntimeStatus) {
			p.Walls = micro
			p.KVM = api.KVM{Present: true, Group: "kvm"}
			p.HasSetfacl = true
		}, false, []RootStepID{RootKVMACL, RootKVMGroup}},
		{"KVM device without driver", func(p *api.CapProbe, _ *api.RuntimeStatus) {
			p.KVM = api.KVM{Present: true, Group: "kvm"}
			p.HasSetfacl = true
		}, false, nil},
		{"Nitro driver cannot justify KVM", func(p *api.CapProbe, _ *api.RuntimeStatus) {
			p.Walls = []api.WallProbe{{Driver: "nitro", Level: "microvm"}}
			p.KVM = api.KVM{Present: true, Group: "kvm"}
			p.HasSetfacl = true
		}, false, nil},
		{"missing ACL tool", func(p *api.CapProbe, _ *api.RuntimeStatus) {
			p.Walls = micro
			p.KVM = api.KVM{Present: true, Group: "kvm"}
		}, false, []RootStepID{RootKVMGroup}},
		{"missing KVM device", func(p *api.CapProbe, _ *api.RuntimeStatus) { p.Walls = micro; p.HasSetfacl = true; p.KVM.Group = "kvm" }, false, nil},
		{"usable KVM still needs group", func(p *api.CapProbe, _ *api.RuntimeStatus) {
			p.Walls = micro
			p.KVM = api.KVM{Present: true, Usable: true, Group: "kvm"}
			p.HasSetfacl = true
		}, false, []RootStepID{RootKVMGroup}},
		{"other device group", func(p *api.CapProbe, _ *api.RuntimeStatus) {
			p.Walls = micro
			p.KVM = api.KVM{Present: true, Group: "other"}
			p.HasSetfacl = true
		}, false, []RootStepID{RootKVMACL}},
		{"group already present", func(p *api.CapProbe, _ *api.RuntimeStatus) {
			p.Walls = micro
			p.KVM = api.KVM{Present: true, Group: "kvm", InGroup: true}
			p.HasSetfacl = true
		}, false, []RootStepID{RootKVMACL}},
		{"linger", func(p *api.CapProbe, _ *api.RuntimeStatus) { p.Linger = false }, false, []RootStepID{RootLinger}},
		{"IO delegation opt in", func(p *api.CapProbe, _ *api.RuntimeStatus) { p.Delegated = []string{"cpu", "memory", "pids"} }, true, []RootStepID{RootDelegateIO}},
		{"IO delegation not requested", func(p *api.CapProbe, _ *api.RuntimeStatus) { p.Delegated = []string{"cpu", "memory", "pids"} }, false, nil},
		{"subuid with mapping tools", func(p *api.CapProbe, _ *api.RuntimeStatus) { p.Subuid = false; p.HasNewuidmap = true }, false, []RootStepID{RootSubuid}},
		{"subuid without mapping tools", func(p *api.CapProbe, _ *api.RuntimeStatus) { p.Subuid = false }, false, nil},
		{"io_uring enabled", func(p *api.CapProbe, _ *api.RuntimeStatus) { p.IOURingDisabled = 0 }, false, []RootStepID{RootIOUringOff}},
		{"io_uring partly disabled", func(p *api.CapProbe, _ *api.RuntimeStatus) { p.IOURingDisabled = 1 }, false, []RootStepID{RootIOUringOff}},
		{"vsock for certified microvm", func(p *api.CapProbe, _ *api.RuntimeStatus) {
			p.VsockBlocked = false
			p.Walls = micro
			p.KVM = api.KVM{Present: true, Group: "kvm", InGroup: true}
		}, false, []RootStepID{RootVsockModules}},
		{"vsock without runtime", func(p *api.CapProbe, r *api.RuntimeStatus) {
			p.VsockBlocked = false
			p.Walls = micro
			p.KVM.Present = true
			r.Drivers = nil
		}, false, nil},
		{"vsock without microvm", func(p *api.CapProbe, _ *api.RuntimeStatus) { p.VsockBlocked = false }, false, nil},
		{"AppArmor is a later dependency", func(p *api.CapProbe, _ *api.RuntimeStatus) { p.UserNS = false; p.UserNSRestricted = true }, false, nil},
		{"nonlinux", func(p *api.CapProbe, _ *api.RuntimeStatus) { p.OS = "darwin"; p.Linger = false }, false, nil},
		{"root account", func(p *api.CapProbe, _ *api.RuntimeStatus) { p.User = "root"; p.Linger = false }, false, nil},
		{"uid zero", func(p *api.CapProbe, _ *api.RuntimeStatus) { p.UID = 0; p.Linger = false }, false, nil},
		{"unsafe user", func(p *api.CapProbe, _ *api.RuntimeStatus) { p.User = "me;id"; p.Linger = false }, false, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p, r := rootSelectionProbe(), rootSelectionRuntime()
			tt.change(&p, &r)
			if got := RootStepsFor(p, r, tt.delegate, 0); !slices.Equal(got, tt.want) {
				t.Errorf("root steps = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDefaultRootStepModeRequiresBothEmptyHistories(t *testing.T) {
	rootTestState(t)
	for _, tier := range []Tier{TierOwn, TierThrowaway, TierShared, TierUnknown} {
		for _, local := range []bool{false, true} {
			for _, remote := range []int{-1, 0, 1, 100} {
				for _, yes := range []bool{false, true} {
					want := RootPrint
					if (tier == TierOwn || tier == TierThrowaway) && !local && remote == 0 && !yes {
						want = RootRun
					}
					got := DefaultRootStepMode(tier, local, api.CapProbe{Started: remote}, yes)
					if got != want {
						t.Errorf("tier=%v local=%v remote=%d yes=%v: %s, want %s", tier, local, remote, yes, got, want)
					}
				}
			}
		}
	}
}

func TestParseRootStepMode(t *testing.T) {
	rootTestState(t)
	for _, value := range []string{"run", "print", "skip"} {
		got, err := ParseRootStepMode(value)
		if err != nil || string(got) != value {
			t.Errorf("ParseRootStepMode(%q) = %q, %v", value, got, err)
		}
	}
	for _, value := range []string{"", "Run", " run", "print ", "sudo", "run;id", "run\n"} {
		_, err := ParseRootStepMode(value)
		assertRootError(t, err)
		var ue *UserError
		if !errors.As(err, &ue) || ue.Kind != KindUsage {
			t.Errorf("invalid mode %q should be a usage error: %v", value, err)
		}
	}
}
