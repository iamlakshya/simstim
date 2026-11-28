package host

import (
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestEmbeddedKernelBugListValid(t *testing.T) {
	list, err := EmbeddedKernelBugList()
	if err != nil {
		t.Fatalf("EmbeddedKernelBugList: %v", err)
	}
	if list.Schema != 1 {
		t.Fatalf("schema = %d, want 1", list.Schema)
	}
	byID := map[string]KernelBug{}
	for _, b := range list.Bugs {
		byID[b.ID] = b
	}
	for _, want := range []struct{ id, name string }{
		{"CVE-2026-53365", "VsockDrop"},
		{"CVE-2026-74580", "Vghost"},
	} {
		b, ok := byID[want.id]
		if !ok {
			t.Fatalf("missing bug %s in kernel_bugs.toml", want.id)
		}
		if b.Name != want.name || b.URL == "" || len(b.Upstream) == 0 || len(b.Distro) == 0 {
			t.Errorf("incomplete entry for %s: %+v", want.id, b)
		}
	}

	if _, err := ParseKernelBugList([]byte("schema = 2\n")); err == nil {
		t.Error("expected unsupported schema error")
	}
	if _, err := ParseKernelBugList([]byte("schema = 1\nextra_field = true\n")); err == nil {
		t.Error("expected unknown field error")
	}
}

func TestCompareDebianVersion(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"6.12.100-1", "6.12.100-1", 0},
		{"0:6.12.100-1", "6.12.100-1", 0},
		{"6.12.48-1", "6.12.100-1", -1},
		{"6.12.118-1", "6.12.100-1", 1},
		{"6.12.9-1", "6.12.10-1", -1},
		{"6.12.100-1~deb12u1", "6.12.100-1", -1},
		{"6.12.100-1~deb12u1", "6.12.100-1~deb12u2", -1},
		{"1.0~~", "1.0~~a", -1},
		{"1.0~~a", "1.0~", -1},
		{"1.0~", "1.0", -1},
		{"1.0", "1.0a", -1},
		{"1.0a", "1.0+", -1},
		{"7.0.0-28.28~24.04.1", "7.0.0-28.28", -1},
		{"6.8.0-45.45", "6.8.0-149.149", -1},
		{"6.8.0-149.149", "6.8.0-149.149", 0},
		{"6.8.0-150.150", "6.8.0-149.149", 1},
		{"1:5.10.0-1", "6.12.118-1", 1},
		{"2:1.0-1", "1:9.9-9", 1},
		{"1.01-1", "1.1-1", 0},
		{"6.12.100", "6.12.100-1", -1},
		{"6.12.100-1", "6.12.100-1+deb13u1", -1},
	}
	for _, tc := range cases {
		if got := CompareDebianVersion(tc.a, tc.b); got != tc.want {
			t.Errorf("CompareDebianVersion(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
		if got := CompareDebianVersion(tc.b, tc.a); got != -tc.want {
			t.Errorf("CompareDebianVersion(%q, %q) = %d, want %d", tc.b, tc.a, got, -tc.want)
		}
	}
}

func TestExtractDistroPkgVersions(t *testing.T) {
	// Recorded uname -v from Debian 13 (host-a).
	if got := ExtractDebianPkgVersion("#1 SMP PREEMPT_DYNAMIC Debian 6.12.118-1 (2026-11-20)"); got != "6.12.118-1" {
		t.Errorf("ExtractDebianPkgVersion(deb13) = %q, want 6.12.118-1", got)
	}
	if got := ExtractDebianPkgVersion("#1 SMP PREEMPT_DYNAMIC Debian 6.12.100-1~deb12u1 (2026-09-10)"); got != "6.12.100-1~deb12u1" {
		t.Errorf("ExtractDebianPkgVersion(deb12-bpo) = %q, want 6.12.100-1~deb12u1", got)
	}
	if got := ExtractDebianPkgVersion("#1 SMP PREEMPT_DYNAMIC Ubuntu"); got != "" {
		t.Errorf("ExtractDebianPkgVersion(non-debian) = %q, want empty", got)
	}

	// Recorded Ubuntu 24.04 /proc/version_signature formats.
	for _, tc := range []struct{ in, want string }{
		{"Ubuntu 6.8.0-45.45-generic 6.8.12", "6.8.0-45.45"},
		{"Ubuntu 6.8.0-45.45-generic-64k 6.8.12", "6.8.0-45.45"},
		{"Ubuntu 6.8.0-45.45-aws-64k 6.8.12", "6.8.0-45.45"},
		{"Ubuntu 6.8.0-149.149-aws 6.8.12", "6.8.0-149.149"},
		{"Ubuntu 7.0.0-28.28~24.04.1-generic 7.0.0", "7.0.0-28.28~24.04.1"},
		{"Ubuntu 6.8.0-45.45-cloud-amd64 6.8.12", "6.8.0-45.45"},
		{"Ubuntu", ""},
	} {
		if got := ExtractUbuntuPkgVersion(tc.in); got != tc.want {
			t.Errorf("ExtractUbuntuPkgVersion(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestEvaluateKernelBugsScenarios(t *testing.T) {
	list, err := EmbeddedKernelBugList()
	if err != nil {
		t.Fatal(err)
	}

	deb13OS := []byte("PRETTY_NAME=\"Debian GNU/Linux 13 (trixie)\"\nID=debian\nVERSION_ID=\"13\"\n")
	deb12OS := []byte("PRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"\nID=debian\nVERSION_ID=\"12\"\n")
	ubuntu2404OS := []byte("PRETTY_NAME=\"Ubuntu 24.04.3 LTS\"\nID=ubuntu\nVERSION_ID=\"24.04\"\n")
	fedora42OS := []byte("PRETTY_NAME=\"Fedora Linux 42\"\nID=fedora\nVERSION_ID=42\n")

	byID := func(res []api.KernelBugResult) map[string]api.KernelBugResult {
		m := map[string]api.KernelBugResult{}
		for _, r := range res {
			m[r.ID] = r
		}
		return m
	}

	t.Run("debian 13 patched (recorded host-a) emits no warnings", func(t *testing.T) {
		checks, res := EvaluateKernelBugs(list, KernelBugEnv{
			GOOS:         "linux",
			UnameR:       "6.12.118+deb13-cloud-amd64",
			UnameV:       "#1 SMP PREEMPT_DYNAMIC Debian 6.12.118-1 (2026-11-20)",
			OSRelease:    deb13OS,
			ModuleLoaded: func(string) bool { return true },
		})
		if len(checks) != 0 {
			t.Fatalf("patched debian 13 checks = %+v, want none", checks)
		}
		m := byID(res)
		if m["CVE-2026-53365"].Status != api.KernelBugFixed || m["CVE-2026-74580"].Status != api.KernelBugFixed {
			t.Fatalf("patched debian 13 results = %+v", res)
		}
	})

	t.Run("debian 13 unpatched with vhost_vsock loaded warns on both", func(t *testing.T) {
		checks, res := EvaluateKernelBugs(list, KernelBugEnv{
			GOOS:         "linux",
			UnameR:       "6.12.48+deb13-amd64",
			UnameV:       "#1 SMP PREEMPT_DYNAMIC Debian 6.12.48-1 (2025-10-01)",
			OSRelease:    deb13OS,
			ModuleLoaded: func(string) bool { return true },
		})
		if len(checks) != 2 {
			t.Fatalf("unpatched debian 13 checks = %+v, want 2 warnings", checks)
		}
		m := byID(res)
		vd := m["CVE-2026-53365"]
		if vd.Status != api.KernelBugAffected || vd.Severity != "warn" {
			t.Fatalf("VsockDrop = %+v, want affected warn", vd)
		}
		wantDetail := "Kernel 6.12.48-1 has VsockDrop (CVE-2026-53365), fixed in package 6.12.100-1. Update the kernel, then reboot."
		if vd.Detail != wantDetail {
			t.Errorf("VsockDrop detail = %q, want %q", vd.Detail, wantDetail)
		}
		if !strings.Contains(vd.Fix, "vsock-modules") {
			t.Errorf("VsockDrop fix = %q, want vsock-modules hint", vd.Fix)
		}
		vg := m["CVE-2026-74580"]
		if vg.Status != api.KernelBugAffected || vg.Severity != "warn" || !strings.Contains(vg.Detail, "6.12.105-1") {
			t.Errorf("Vghost = %+v", vg)
		}
	})

	t.Run("debian 13 unpatched with vhost_vsock absent marks VsockDrop mitigated", func(t *testing.T) {
		checks, res := EvaluateKernelBugs(list, KernelBugEnv{
			GOOS:         "linux",
			UnameR:       "6.12.48+deb13-amd64",
			UnameV:       "#1 SMP PREEMPT_DYNAMIC Debian 6.12.48-1 (2025-10-01)",
			OSRelease:    deb13OS,
			ModuleLoaded: func(name string) bool { return name != "vhost_vsock" },
		})
		m := byID(res)
		vd := m["CVE-2026-53365"]
		if vd.Status != api.KernelBugMitigated || vd.Severity != "ok" {
			t.Fatalf("VsockDrop mitigated = %+v", vd)
		}
		want := "VsockDrop (CVE-2026-53365): mitigated, vhost_vsock is not loaded."
		if vd.Detail != want {
			t.Errorf("detail = %q, want %q", vd.Detail, want)
		}
		if len(checks) != 2 || !checks[0].OK || checks[1].OK {
			t.Fatalf("checks = %+v, want [OK mitigated, !OK affected]", checks)
		}
	})

	t.Run("debian 12 stock 6.1 skips 6.12 backports row for VsockDrop", func(t *testing.T) {
		checks, res := EvaluateKernelBugs(list, KernelBugEnv{
			GOOS:         "linux",
			UnameR:       "6.1.0-35-amd64",
			UnameV:       "#1 SMP PREEMPT_DYNAMIC Debian 6.1.190-1 (2026-10-20)",
			OSRelease:    deb12OS,
			ModuleLoaded: func(string) bool { return true },
		})
		if len(checks) != 0 {
			t.Fatalf("debian 12 stock 6.1.190-1 checks = %+v, want none", checks)
		}
		m := byID(res)
		if m["CVE-2026-53365"].Status != api.KernelBugFixed || m["CVE-2026-74580"].Status != api.KernelBugFixed {
			t.Fatalf("debian 12 stock results = %+v", res)
		}
	})

	t.Run("ubuntu 24.04 GA matches distro row for Vghost and upstream maybe for VsockDrop", func(t *testing.T) {
		checks, res := EvaluateKernelBugs(list, KernelBugEnv{
			GOOS:             "linux",
			UnameR:           "6.8.12-45-generic",
			VersionSignature: "Ubuntu 6.8.0-45.45-generic 6.8.12",
			OSRelease:        ubuntu2404OS,
			ModuleLoaded:     func(string) bool { return true },
		})
		m := byID(res)
		if m["CVE-2026-53365"].Status != api.KernelBugMaybe || !strings.Contains(m["CVE-2026-53365"].Detail, "Kernel 6.8.12 is in the range of VsockDrop (CVE-2026-53365)") {
			t.Errorf("ubuntu VsockDrop = %+v", m["CVE-2026-53365"])
		}
		if m["CVE-2026-74580"].Status != api.KernelBugAffected || !strings.Contains(m["CVE-2026-74580"].Detail, "6.8.0-149.149") {
			t.Errorf("ubuntu Vghost = %+v", m["CVE-2026-74580"])
		}
		if len(checks) != 2 || checks[0].OK || checks[1].OK {
			t.Errorf("ubuntu checks = %+v", checks)
		}

		// Patched Ubuntu 24.04 GA with vhost_vsock absent: Vghost fixed, VsockDrop mitigated.
		checks2, res2 := EvaluateKernelBugs(list, KernelBugEnv{
			GOOS:             "linux",
			UnameR:           "6.8.12-149-generic",
			VersionSignature: "Ubuntu 6.8.0-149.149-generic 6.8.12",
			OSRelease:        ubuntu2404OS,
			ModuleLoaded:     func(string) bool { return false },
		})
		m2 := byID(res2)
		if m2["CVE-2026-74580"].Status != api.KernelBugFixed || m2["CVE-2026-53365"].Status != api.KernelBugMitigated {
			t.Errorf("patched ubuntu results = %+v", res2)
		}
		if len(checks2) != 1 || !checks2[0].OK {
			t.Errorf("patched ubuntu checks = %+v, want only mitigated ok", checks2)
		}
	})

	t.Run("ubuntu 24.04 arm64 64k flavour reports unpatched Vghost as affected", func(t *testing.T) {
		checks, res := EvaluateKernelBugs(list, KernelBugEnv{
			GOOS:             "linux",
			UnameR:           "6.8.12-45-generic-64k",
			VersionSignature: "Ubuntu 6.8.0-45.45-generic-64k 6.8.12",
			OSRelease:        ubuntu2404OS,
			ModuleLoaded:     func(string) bool { return true },
		})
		m := byID(res)
		if m["CVE-2026-74580"].Status != api.KernelBugAffected || !strings.Contains(m["CVE-2026-74580"].Detail, "6.8.0-149.149") {
			t.Errorf("ubuntu 64k Vghost = %+v, want affected (fixed in 6.8.0-149.149)", m["CVE-2026-74580"])
		}
		if m["CVE-2026-53365"].Status != api.KernelBugMaybe {
			t.Errorf("ubuntu 64k VsockDrop = %+v, want maybe", m["CVE-2026-53365"])
		}
		if len(checks) != 2 || checks[0].OK || checks[1].OK {
			t.Errorf("ubuntu 64k checks = %+v, want 2 warnings", checks)
		}
	})

	t.Run("debian 13 6.18 backport skips 6.12 distro row and falls back to upstream maybe", func(t *testing.T) {
		checks, res := EvaluateKernelBugs(list, KernelBugEnv{
			GOOS:         "linux",
			UnameR:       "6.18.10+deb13-amd64",
			UnameV:       "#1 SMP PREEMPT_DYNAMIC Debian 6.18.10-1~bpo13+1 (2026-08-01)",
			OSRelease:    deb13OS,
			ModuleLoaded: func(string) bool { return true },
		})
		m := byID(res)
		if m["CVE-2026-53365"].Status != api.KernelBugMaybe {
			t.Errorf("debian 13 6.18 bpo VsockDrop = %+v, want maybe", m["CVE-2026-53365"])
		}
		if m["CVE-2026-74580"].Status != api.KernelBugMaybe {
			t.Errorf("debian 13 6.18 bpo Vghost = %+v, want maybe", m["CVE-2026-74580"])
		}
		if len(checks) != 2 || checks[0].OK || checks[1].OK {
			t.Errorf("debian 13 6.18 bpo checks = %+v, want 2 warnings", checks)
		}
	})

	t.Run("other distro (Fedora 42) uses upstream maybe and fixed", func(t *testing.T) {
		_, resMaybe := EvaluateKernelBugs(list, KernelBugEnv{
			GOOS:         "linux",
			UnameR:       "6.12.48-200.fc42.x86_64",
			OSRelease:    fedora42OS,
			ModuleLoaded: func(string) bool { return true },
		})
		m := byID(resMaybe)
		if m["CVE-2026-53365"].Status != api.KernelBugMaybe || m["CVE-2026-74580"].Status != api.KernelBugMaybe {
			t.Fatalf("fedora unpatched = %+v", resMaybe)
		}

		checksFixed, resFixed := EvaluateKernelBugs(list, KernelBugEnv{
			GOOS:         "linux",
			UnameR:       "6.18.50-100.fc42.x86_64",
			OSRelease:    fedora42OS,
			ModuleLoaded: func(string) bool { return true },
		})
		if len(checksFixed) != 0 {
			t.Fatalf("fedora patched checks = %+v, want none", checksFixed)
		}
		mf := byID(resFixed)
		if mf["CVE-2026-53365"].Status != api.KernelBugFixed || mf["CVE-2026-74580"].Status != api.KernelBugFixed {
			t.Fatalf("fedora patched = %+v", resFixed)
		}
	})

	t.Run("fact-based mitigation condition holds only when fact matches", func(t *testing.T) {
		custom := KernelBugList{
			Schema: 1,
			Bugs: []KernelBug{{
				ID:          "CVE-2026-99991",
				Name:        "TestBug",
				MitigatedBy: []string{"io_uring_disabled=2"},
				Upstream:    []KernelBugRange{{From: "6.12.0", Fixed: "6.12.99"}},
			}},
		}
		_, resHit := EvaluateKernelBugs(custom, KernelBugEnv{
			GOOS:   "linux",
			UnameR: "6.12.50",
			Facts:  map[string]string{"io_uring_disabled": "2"},
		})
		if len(resHit) != 1 || resHit[0].Status != api.KernelBugMitigated {
			t.Fatalf("mitigated by fact = %+v", resHit)
		}
		_, resMiss := EvaluateKernelBugs(custom, KernelBugEnv{
			GOOS:   "linux",
			UnameR: "6.12.50",
			Facts:  map[string]string{"io_uring_disabled": "0"},
		})
		if len(resMiss) != 1 || resMiss[0].Status != api.KernelBugMaybe {
			t.Fatalf("unmitigated by fact = %+v", resMiss)
		}
	})

	t.Run("unknown kernel version produces ok info check", func(t *testing.T) {
		checks, res := EvaluateKernelBugs(list, KernelBugEnv{
			GOOS:   "linux",
			UnameR: "custom-kernel",
		})
		if len(checks) != 1 || !checks[0].OK || !strings.Contains(checks[0].Detail, "could not be read") {
			t.Fatalf("unknown checks = %+v", checks)
		}
		if len(res) != 1 || res[0].Status != api.KernelBugUnknown {
			t.Fatalf("unknown res = %+v", res)
		}
	})

	t.Run("non-linux skips kernel bug list", func(t *testing.T) {
		checks, res := EvaluateKernelBugs(list, KernelBugEnv{GOOS: "darwin", UnameR: "24.3.0"})
		if len(checks) != 0 || len(res) != 0 {
			t.Fatalf("darwin checks=%+v res=%+v, want empty", checks, res)
		}
	})
}

// Spec 20.11 and question 7: doctor warns on known local root bugs and never
// lowers a host's wall floor or changes its wall level.
func TestKernelBugWarningNeverLowersFloor(t *testing.T) {
	list, err := EmbeddedKernelBugList()
	if err != nil {
		t.Fatal(err)
	}
	probe := api.CapProbe{
		OS:     "linux",
		Arch:   "amd64",
		Kernel: "6.12.48+deb13-amd64",
		KVM:    api.KVM{Present: true, Usable: true},
		Bwrap:  true, UserNS: true, LandlockABI: 6, Seccomp: true,
	}
	rt := api.RuntimeStatus{Items: []api.RuntimeItem{
		{Kind: "vmm", Name: "cloud-hypervisor", Arch: "amd64", HostKernel: ">=6.8"},
	}}
	nowBefore, afterBefore := api.LevelsFor(probe, rt)

	checks, res := EvaluateKernelBugs(list, KernelBugEnv{
		GOOS:         "linux",
		UnameR:       probe.Kernel,
		UnameV:       "#1 SMP PREEMPT_DYNAMIC Debian 6.12.48-1 (2025-10-01)",
		OSRelease:    []byte("ID=debian\nVERSION_ID=\"13\"\n"),
		ModuleLoaded: func(string) bool { return true },
	})
	if len(checks) == 0 || len(res) == 0 || res[0].Status != api.KernelBugAffected {
		t.Fatalf("expected affected kernel warning, got checks=%+v res=%+v", checks, res)
	}

	nowAfter, afterAfter := api.LevelsFor(probe, rt)
	if nowBefore != api.LevelMicroVM || nowAfter != nowBefore || afterAfter != afterBefore {
		t.Fatalf("kernel bug warning changed levels: before=(%s,%s) after=(%s,%s)", nowBefore, afterBefore, nowAfter, afterAfter)
	}
}
