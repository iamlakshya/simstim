package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/version"
	"github.com/iamlakshya/simstim/internal/wall"
)

func TestHostShowAndDoctorSandboxes(t *testing.T) {
	newTestHost(t)

	out, hints, err := runApart(t, "host", "doctor", "box")
	if err != nil {
		t.Fatalf("host doctor: %v", err)
	}
	if !strings.Contains(out, "Sandboxes on box:\n") || !strings.Contains(out, "Level now: ") {
		t.Errorf("host doctor lacks Sandboxes block:\nstdout:\n%s\nstderr:\n%s", out, hints)
	}

	rawJSON := okJSON(t, "host", "doctor", "box", "--json")
	checkJSON(t, "host doctor box --json", rawJSON, false)
	var d client.HostInfo
	if err := json.Unmarshal([]byte(rawJSON), &d); err != nil {
		t.Fatalf("decode host doctor --json: %v", err)
	}
	if d.Sandbox == nil || d.Sandbox.OS != runtime.GOOS || len(d.Sandbox.Walls) == 0 {
		t.Errorf("host doctor --json sandbox = %+v", d.Sandbox)
	}

	// Verify sandbox.probe wrote walls.json cache and left artefacts/incoming empty.
	cached, okCache := wall.ReadCache(paths.WallProbes(), host.BootID(), version.Version)
	if !okCache || len(cached) == 0 {
		t.Errorf("walls.json cache missing or invalid: ok=%v len=%d", okCache, len(cached))
	}
	if entries, err := os.ReadDir(paths.ArtefactsIncomingDir()); err == nil && len(entries) != 0 {
		t.Errorf("artefacts/incoming left temporary files behind: %+v", entries)
	}

	// host show prints saved settings, sandbox block and wall drivers.
	showOut, _, err := runApart(t, "host", "show", "box")
	if err != nil {
		t.Fatalf("host show: %v", err)
	}
	for _, want := range []string{"box\n", "Target", "Sandboxes on box:", "Wall drivers on box:", "process"} {
		if !strings.Contains(showOut, want) {
			t.Errorf("host show lacks %q:\n%s", want, showOut)
		}
	}

	showJSON := okJSON(t, "host", "show", "box", "--json")
	checkJSON(t, "host show box --json", showJSON, false)
	var obj map[string]any
	if err := json.Unmarshal([]byte(showJSON), &obj); err != nil {
		t.Fatalf("decode host show --json: %v", err)
	}
	if obj["reachable"] != true || obj["sandbox"] == nil {
		t.Errorf("host show --json = %+v", obj)
	}
}

func TestHostSetFloor(t *testing.T) {
	th := newTestHost(t)

	// Setting floor above what the host supports fails with exit code 5 (wall_floor).
	got, code := lineErr(t, "host", "set", "box", "floor=microvm")
	if code != 5 || !strings.Contains(got, "cannot be microvm") {
		t.Errorf("host set floor=microvm: exit %d\n%s", code, got)
	}

	// Bad values and unknown keys fail with exit code 2 and suggestions.
	if got, code := lineErr(t, "host", "set", "box", "floor=bogus"); code != 2 || !strings.Contains(got, "unknown wall level") {
		t.Errorf("host set floor=bogus: exit %d\n%s", code, got)
	}
	if got, code := lineErr(t, "host", "set", "box", "flor=none"); code != 2 || !strings.Contains(got, `Did you mean "floor"?`) {
		t.Errorf("host set flor=none: exit %d\n%s", code, got)
	}
	if got, code := lineErr(t, "host", "set", "box"); code != 2 || !strings.Contains(got, "needs a setting") {
		t.Errorf("host set without pairs: exit %d\n%s", code, got)
	}

	// floor=none succeeds on any host.
	if msg := okSaid(t, "host", "set", "box", "floor=none"); !strings.Contains(msg, "box: floor none → none.") {
		t.Errorf("host set floor=none = %q", msg)
	}
	if h, err := client.FindHost("box"); err != nil || h.WallFloor != "none" {
		t.Errorf("saved floor = %q, %v", h.WallFloor, err)
	}

	// With a working bwrap on PATH on Linux (where Landlock and seccomp work),
	// floor=process succeeds and writes wall_floor = "process" to hosts.toml.
	if runtime.GOOS == "linux" {
		th.h.Script("bwrap", `[ "$1" = --version ] && echo "bubblewrap 0.11.0"; exit 0`)
		t.Setenv("PATH", filepath.Join(th.h.Root, "bin")+":"+os.Getenv("PATH"))

		// Confirm re-probing overwrites the stale walls.json cache.
		msg := okSaid(t, "host", "set", "box", "floor=process")
		if !strings.Contains(msg, "box: floor none → process.") {
			t.Errorf("host set floor=process = %q", msg)
		}
		h, err := client.FindHost("box")
		if err != nil || h.WallFloor != "process" {
			t.Fatalf("saved floor = %q, %v", h.WallFloor, err)
		}
		cached, okCache := wall.ReadCache(paths.WallProbes(), host.BootID(), version.Version)
		if !okCache || wall.Best(cached) != wall.Process {
			t.Errorf("walls.json cache was not refreshed after bwrap became available: ok=%v best=%v", okCache, wall.Best(cached))
		}
	}
}

func TestHostAddPinsBestFloor(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("process wall probe requires Linux")
	}
	th := newTestHost(t)
	th.h.Script("bwrap", `[ "$1" = --version ] && echo "bubblewrap 0.11.0"; exit 0`)
	t.Setenv("PATH", filepath.Join(th.h.Root, "bin")+":"+os.Getenv("PATH"))
	th.h.Install(client.HelperVersion(), "")

	added := th.ch
	added.Name = "pinned"
	ctx := context.Background()
	out, err := said(t, func() error { return addHost(ctx, quietPrompts(ctx), added, true) })
	if err != nil {
		t.Fatalf("addHost: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Sandboxes on pinned:") || !strings.Contains(out, "Level now: process (floor: process).") {
		t.Errorf("addHost output:\n%s", out)
	}
	h, err := client.FindHost("pinned")
	if err != nil || h.WallFloor != "process" {
		t.Errorf("pinned floor = %q, %v", h.WallFloor, err)
	}

	// Lower floor named by user is kept.
	lower := th.ch
	lower.Name = "lower"
	lower.WallFloor = "none"
	if _, err := said(t, func() error { return addHost(ctx, quietPrompts(ctx), lower, true) }); err != nil {
		t.Fatalf("addHost --floor none: %v", err)
	}
	if h, err := client.FindHost("lower"); err != nil || h.WallFloor != "none" {
		t.Errorf("lower floor = %q, %v", h.WallFloor, err)
	}

	// Floor above host capability prints the Sandboxes block, refuses to save,
	// and suggests --floor rather than host doctor for the unsaved host.
	tooHigh := th.ch
	tooHigh.Name = "too-high"
	tooHigh.WallFloor = "microvm"
	highOut, highErr := said(t, func() error { return addHost(ctx, quietPrompts(ctx), tooHigh, true) })
	var ue *client.UserError
	if !errors.As(highErr, &ue) || ue.Code != "wall_floor" {
		t.Fatalf("addHost --floor microvm err = %v, want wall_floor", highErr)
	}
	if !strings.Contains(highOut, "Sandboxes on too-high:") {
		t.Errorf("addHost --floor microvm output lacks Sandboxes block:\n%s", highOut)
	}
	if !strings.Contains(ue.Hint, "--floor process") || strings.Contains(ue.Hint, "host doctor") {
		t.Errorf("addHost --floor microvm hint = %q, want --floor process without host doctor", ue.Hint)
	}
	if _, err := client.FindHost("too-high"); err == nil {
		t.Errorf("too-high should not be saved when --floor fails")
	}
}

func TestPrintSandboxesBlockHints(t *testing.T) {
	var rows, hints bytes.Buffer
	p := api.CapProbe{
		OS: "linux", Arch: "amd64", Kernel: "6.8.0-45-generic",
		Systemd: 255, CgroupV2: true, Delegated: []string{"cpu", "memory", "pids"},
		User: "me", KVM: api.KVM{Present: true, Usable: false, Mode: "crw-rw----", Group: "kvm"},
		CPUVirt: "vmx", Bwrap: true, BwrapVersion: "0.9.0",
		LandlockABI: 4, Seccomp: true, UserNS: false, UserNSRestricted: true,
		DiskFree: 50 << 30, DiskFS: "xfs", Reflink: true,
		Walls: []api.WallProbe{{
			Driver: "process", Level: "process", OK: false,
			Reason: "Ubuntu's AppArmor user-namespace restriction blocks bubblewrap (kernel.apparmor_restrict_unprivileged_userns = 1)",
			Fix:    "sudo apt install apparmor-profiles && sudo cp /usr/share/apparmor/extra-profiles/bwrap-userns-restrict /etc/apparmor.d/ && sudo apparmor_parser -r /etc/apparmor.d/bwrap-userns-restrict",
		}},
	}
	printSandboxesBlock(&rows, &hints, "lab", p, "none")
	for _, want := range []string{
		"Sandboxes on lab:",
		"✓ kernel 6.8.0-45-generic, systemd 255, cgroup v2 with cpu, memory, pids",
		"✓ /dev/kvm present, Intel VT-x",
		"✕ /dev/kvm not open to me (crw-rw---- root kvm)",
		"✓ bubblewrap 0.9.0",
		"✕ user namespaces are restricted by AppArmor",
		"xfs with reflink",
		"Level now: none (floor: none). After setup: process.",
	} {
		if !strings.Contains(rows.String(), want) {
			t.Errorf("rows lacks %q:\n%s", want, rows.String())
		}
	}
	if !strings.Contains(hints.String(), "Try: sudo apt install apparmor-profiles") || !strings.Contains(hints.String(), "Try: sudo usermod -aG kvm me") {
		t.Errorf("hints =\n%s", hints.String())
	}

	// Also verify host show synonym suggestions work.
	if got, code := lineErr(t, "host", "info", "box"); code != 2 || !strings.Contains(got, `Did you mean "show"?`) || !strings.Contains(got, "Try: "+brand.Name+" host show box") {
		t.Errorf("host info synonym: exit %d\n%s", code, got)
	}
}

func TestPrintSandboxesBlockEdgeCases(t *testing.T) {
	t.Run("shell metacharacters in KVM user and group are quoted", func(t *testing.T) {
		var rows, hints bytes.Buffer
		p := api.CapProbe{
			OS:         "linux",
			User:       "me;id",
			HasSetfacl: true,
			KVM:        api.KVM{Present: true, Usable: false, Mode: "crw-rw----", Group: "kvm$(whoami)"},
		}
		printKVMLines(&rows, &hints, p)
		got := hints.String()
		want := "Try: sudo setfacl -m 'u:me;id:rw' /dev/kvm && sudo usermod -aG 'kvm$(whoami)' 'me;id'"
		if !strings.Contains(got, want) {
			t.Errorf("hints = %q, want substring %q", got, want)
		}
	})

	t.Run("empty KVM user keeps expandable $USER", func(t *testing.T) {
		var rows, hints bytes.Buffer
		p := api.CapProbe{
			OS:         "linux",
			HasSetfacl: true,
			KVM:        api.KVM{Present: true, Usable: false, Mode: "crw-rw----", Group: "kvm"},
		}
		printKVMLines(&rows, &hints, p)
		got := hints.String()
		want := `Try: sudo setfacl -m u:"$USER":rw /dev/kvm && sudo usermod -aG kvm "$USER"`
		if !strings.Contains(got, want) {
			t.Errorf("hints = %q, want substring %q", got, want)
		}
	})

	t.Run("bwrap with missing Landlock and working userns has no duplicate rows", func(t *testing.T) {
		var rows, hints bytes.Buffer
		p := api.CapProbe{
			OS: "linux", Kernel: "6.12.0",
			Bwrap: true, BwrapVersion: "0.11.0",
			LandlockABI: 0, Seccomp: true, UserNS: true,
			Walls: []api.WallProbe{{
				Driver: "process", Level: "process", OK: false,
				Reason: "Landlock is not enabled on this kernel",
				Fix:    "boot a kernel with Landlock enabled (CONFIG_SECURITY_LANDLOCK and lsm=landlock)",
			}},
		}
		printProcessWallLines(&rows, &hints, p)
		if n := strings.Count(rows.String(), "Landlock is not enabled"); n != 1 {
			t.Errorf("expected 1 Landlock failure row, got %d:\n%s", n, rows.String())
		}
		if n := strings.Count(hints.String(), "Try:"); n != 1 {
			t.Errorf("expected 1 Try: hint, got %d:\n%s", n, hints.String())
		}
	})

	t.Run("bwrap with missing Landlock and failing userns does not reuse Landlock fix for userns", func(t *testing.T) {
		var rows, hints bytes.Buffer
		p := api.CapProbe{
			OS: "linux", Kernel: "6.12.0",
			Bwrap: true, BwrapVersion: "0.11.0",
			LandlockABI: 0, Seccomp: true, UserNS: false, UserNSRestricted: true,
			Walls: []api.WallProbe{{
				Driver: "process", Level: "process", OK: false,
				Reason: "Landlock is not enabled on this kernel",
				Fix:    "boot a kernel with Landlock enabled (CONFIG_SECURITY_LANDLOCK and lsm=landlock)",
			}},
		}
		printProcessWallLines(&rows, &hints, p)
		if !strings.Contains(hints.String(), "apparmor-profiles") {
			t.Errorf("expected AppArmor hint for restricted userns, got:\n%s", hints.String())
		}
		if strings.Count(hints.String(), "sudo") != 1 {
			t.Errorf("expected single sudo command in AppArmor fallback hint, got:\n%s", hints.String())
		}
		if strings.Contains(hints.String(), "CONFIG_SECURITY_LANDLOCK") {
			t.Errorf("Landlock proc.Fix leaked into userns hint:\n%s", hints.String())
		}
	})

	t.Run("Ubuntu 24.04 missing bwrap prints one sudo command Try hint", func(t *testing.T) {
		var rows, hints bytes.Buffer
		wantCmd := "sudo sh -c 'apt-get update && apt-get install -y bubblewrap apparmor-profiles apparmor-utils && install -m 0644 /usr/share/apparmor/extra-profiles/bwrap-userns-restrict /etc/apparmor.d/bwrap-userns-restrict && apparmor_parser -r /etc/apparmor.d/bwrap-userns-restrict'"
		p := api.CapProbe{
			OS: "linux", Kernel: "6.8.0-45-generic",
			Bwrap: false, LandlockABI: 4, Seccomp: true,
			UserNS: false, UserNSRestricted: true,
			Walls: []api.WallProbe{{
				Driver: "process", Level: "process", OK: false,
				Reason: "bubblewrap (bwrap) is not installed",
				Fix:    wantCmd,
			}},
		}
		printProcessWallLines(&rows, &hints, p)
		if !strings.Contains(rows.String(), "bubblewrap (bwrap) is not installed") {
			t.Errorf("rows =\n%s", rows.String())
		}
		if !strings.Contains(hints.String(), "Try: "+wantCmd) {
			t.Errorf("hints =\n%s", hints.String())
		}
		if strings.Count(hints.String(), "sudo") != 1 {
			t.Errorf("expected one sudo command in hints, got:\n%s", hints.String())
		}
	})

	t.Run("non-Linux host prints single Linux requirement row", func(t *testing.T) {
		var rows, hints bytes.Buffer
		p := api.CapProbe{OS: "darwin", Arch: "arm64"}
		printProcessWallLines(&rows, &hints, p)
		if !strings.Contains(rows.String(), "✕ process walls require Linux") {
			t.Errorf("rows =\n%s", rows.String())
		}
		if strings.Contains(rows.String(), "bubblewrap") || strings.Contains(rows.String(), "Landlock") {
			t.Errorf("unexpected Linux-specific rows on darwin:\n%s", rows.String())
		}
		if !strings.Contains(hints.String(), "Lima VM") {
			t.Errorf("hints =\n%s", hints.String())
		}
	})

	t.Run("gVisor OK prints version and platform", func(t *testing.T) {
		var rows, hints bytes.Buffer
		p := api.CapProbe{
			OS: "linux",
			Walls: []api.WallProbe{{
				Driver:  "gvisor",
				Level:   api.LevelGVisor,
				Version: "release-20261123.1",
				OK:      true,
				Facts:   map[string]string{"platform": "systrap", "runsc": "/opt/gvisor/runsc"},
			}},
		}
		printGVisorLine(&rows, &hints, p)
		if !strings.Contains(rows.String(), "✓ gVisor release-20261123.1 (systrap)") {
			t.Errorf("rows =\n%s", rows.String())
		}
		if hints.Len() != 0 {
			t.Errorf("unexpected hints:\n%s", hints.String())
		}
	})

	t.Run("gVisor installed but blocked by AppArmor prints failure and Try sudo fix", func(t *testing.T) {
		var rows, hints bytes.Buffer
		fixCmd := `sudo sh -c 'printf "%s" "$1" > "$2" && chmod 0644 "$2" && apparmor_parser -r "$2"' _ 'profile' '/etc/apparmor.d/` + brand.Name + `-runsc'`
		p := api.CapProbe{
			OS: "linux",
			Walls: []api.WallProbe{{
				Driver: "gvisor",
				Level:  api.LevelGVisor,
				OK:     false,
				Reason: "Ubuntu's AppArmor user-namespace restriction blocks runsc (kernel.apparmor_restrict_unprivileged_userns = 1)",
				Fix:    fixCmd,
				Facts:  map[string]string{"platform": "systrap", "runsc": "/opt/gvisor/runsc"},
			}},
		}
		printSandboxesBlock(&rows, &hints, "ubuntu-vps", p, "process")
		if !strings.Contains(rows.String(), "✕ gVisor: Ubuntu's AppArmor user-namespace restriction blocks runsc") {
			t.Errorf("rows =\n%s", rows.String())
		}
		if !strings.Contains(hints.String(), "Try: "+fixCmd) {
			t.Errorf("hints =\n%s", hints.String())
		}
	})

	t.Run("gVisor not installed stays quiet in Sandboxes block", func(t *testing.T) {
		var rows, hints bytes.Buffer
		p := api.CapProbe{
			OS: "linux",
			Walls: []api.WallProbe{{
				Driver: "gvisor",
				Level:  api.LevelGVisor,
				OK:     false,
				Reason: "pinned runsc release-20261123.1 is not installed in the artefact store",
				Fix:    brand.HostBinary + " wall install gvisor",
				Facts:  map[string]string{"platform": "systrap"},
			}},
		}
		printGVisorLine(&rows, &hints, p)
		if rows.Len() != 0 || hints.Len() != 0 {
			t.Errorf("expected no output when runsc is absent; rows=%q hints=%q", rows.String(), hints.String())
		}
	})

	t.Run("microvm OK prints Cloud Hypervisor version", func(t *testing.T) {
		var rows bytes.Buffer
		p := api.CapProbe{
			OS: "linux",
			Walls: []api.WallProbe{{
				Driver:  "chv",
				Level:   api.LevelMicroVM,
				Version: "55.0",
				OK:      true,
			}},
		}
		printMicroVMLine(&rows, p)
		if !strings.Contains(rows.String(), "✓ Cloud Hypervisor 55.0") {
			t.Errorf("rows = %q", rows.String())
		}
	})

	t.Run("host show names missing optional acl and uidmap packages on KVM host", func(t *testing.T) {
		var rows, hints bytes.Buffer
		p := api.CapProbe{
			OS:           "linux",
			KVM:          api.KVM{Present: true, Usable: false},
			HasSetfacl:   false,
			HasNewuidmap: false,
		}
		printMissingHostPackages(&rows, &hints, p)
		if !strings.Contains(rows.String(), "Optional host packages missing: acl, uidmap") {
			t.Errorf("rows = %q", rows.String())
		}
		if !strings.Contains(hints.String(), "Try: sudo apt install acl uidmap") {
			t.Errorf("hints = %q", hints.String())
		}
	})
}

func TestPrintHostSandboxSectionErrors(t *testing.T) {
	h := client.Host{Name: "box"}

	t.Run("old helper without sandbox feature", func(t *testing.T) {
		var rows, hints bytes.Buffer
		printHostSandboxSection(&rows, &hints, h, false, nil, nil)
		if !strings.Contains(rows.String(), "too old for sandboxes") {
			t.Errorf("rows =\n%s", rows.String())
		}
		if !strings.Contains(hints.String(), brand.Name+" host upgrade box") {
			t.Errorf("hints =\n%s", hints.String())
		}
	})

	t.Run("sandbox feature present but CapProbe failed", func(t *testing.T) {
		var rows, hints bytes.Buffer
		printHostSandboxSection(&rows, &hints, h, true, nil, context.DeadlineExceeded)
		if strings.Contains(rows.String(), "too old for sandboxes") {
			t.Errorf("misreported probe failure as old helper:\n%s", rows.String())
		}
		if !strings.Contains(rows.String(), "sandbox probe failed:") {
			t.Errorf("rows =\n%s", rows.String())
		}
		if !strings.Contains(hints.String(), "Try:") {
			t.Errorf("hints =\n%s", hints.String())
		}
	})
}
