package host

import (
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func kernelCatalogTestBugV1(t *testing.T, id string) kernelBugV1 {
	t.Helper()
	catalog, err := loadKernelBugCatalogV1()
	if err != nil {
		t.Fatal(err)
	}
	for _, bug := range catalog.Bugs {
		if bug.ID == id {
			return bug
		}
	}
	t.Fatal("missing embedded advisory", id)
	return kernelBugV1{}
}

func TestKernelBugUpstreamBoundariesPreserveEveryStableBranchFix(t *testing.T) {
	for _, test := range []struct{ id, release, want string }{
		// CVE-2026-53365 (VsockDrop): CNA initial introduction at 6.7.0 preserved;
		// subsequent disjoint branches include recognised pre/rc candidates via -pre0;
		// 7.1 mainline fixed at 7.1.0-rc5 (Debian tracker 7.1-rc5); 7.2-rc4 stays fixed.
		{"CVE-2026-53365", "6.6.99", "fixed"},
		{"CVE-2026-53365", "6.7.0-pre1", "fixed"},
		{"CVE-2026-53365", "6.7.0-rc1", "fixed"},
		{"CVE-2026-53365", "6.7.0", "maybe"},
		{"CVE-2026-53365", "6.12.96", "maybe"},
		{"CVE-2026-53365", "6.12.97-pre1", "maybe"},
		{"CVE-2026-53365", "6.12.97-rc1", "maybe"},
		{"CVE-2026-53365", "6.12.97", "fixed"},
		{"CVE-2026-53365", "6.12.100", "fixed"},
		{"CVE-2026-53365", "6.13.0-pre1", "maybe"},
		{"CVE-2026-53365", "6.13.0-rc1", "maybe"},
		{"CVE-2026-53365", "6.13-rc1", "maybe"},
		{"CVE-2026-53365", "6.13.0", "maybe"},
		{"CVE-2026-53365", "6.18.33", "maybe"},
		{"CVE-2026-53365", "6.18.34-rc1", "maybe"},
		{"CVE-2026-53365", "6.18.34", "fixed"},
		{"CVE-2026-53365", "6.18.35", "fixed"},
		{"CVE-2026-53365", "6.19.0-pre1", "maybe"},
		{"CVE-2026-53365", "6.19.0-rc1", "maybe"},
		{"CVE-2026-53365", "6.19.0", "maybe"},
		{"CVE-2026-53365", "7.0.0-rc1", "maybe"},
		{"CVE-2026-53365", "7.0.10", "maybe"},
		{"CVE-2026-53365", "7.0.11-rc1", "maybe"},
		{"CVE-2026-53365", "7.0.11", "fixed"},
		{"CVE-2026-53365", "7.0.12", "fixed"},
		{"CVE-2026-53365", "7.1.0-pre1", "maybe"},
		{"CVE-2026-53365", "7.1.0-rc1", "maybe"},
		{"CVE-2026-53365", "7.1-rc4", "maybe"},
		{"CVE-2026-53365", "7.1.0-rc4", "maybe"},
		{"CVE-2026-53365", "7.1.0-rc4-amd64", "maybe"},
		{"CVE-2026-53365", "7.1-rc5", "fixed"},
		{"CVE-2026-53365", "7.1.0-rc5", "fixed"},
		{"CVE-2026-53365", "7.1.0-rc6", "fixed"},
		{"CVE-2026-53365", "7.1.0", "fixed"},
		{"CVE-2026-53365", "7.2.0-rc4", "fixed"},
		{"CVE-2026-53365", "7.2-rc4", "fixed"},

		// CVE-2026-74580 (Vghost): CNA initial introduction at 4.11.0 preserved;
		// subsequent disjoint branches and 7.2 candidates (rc1..rc3+) use -pre0.
		{"CVE-2026-74580", "4.10.99", "fixed"},
		{"CVE-2026-74580", "4.11.0-rc1", "fixed"},
		{"CVE-2026-74580", "4.11.0", "maybe"},
		{"CVE-2026-74580", "5.10.264", "maybe"},
		{"CVE-2026-74580", "5.10.265-rc1", "maybe"},
		{"CVE-2026-74580", "5.10.265", "fixed"},
		{"CVE-2026-74580", "5.10.266", "fixed"},
		{"CVE-2026-74580", "5.11.0-rc1", "maybe"},
		{"CVE-2026-74580", "5.11.0", "maybe"},
		{"CVE-2026-74580", "5.15.215", "maybe"},
		{"CVE-2026-74580", "5.15.216-rc1", "maybe"},
		{"CVE-2026-74580", "5.15.216", "fixed"},
		{"CVE-2026-74580", "5.15.217", "fixed"},
		{"CVE-2026-74580", "5.16.0-rc1", "maybe"},
		{"CVE-2026-74580", "5.16.0", "maybe"},
		{"CVE-2026-74580", "6.1.182", "maybe"},
		{"CVE-2026-74580", "6.1.183-rc1", "maybe"},
		{"CVE-2026-74580", "6.1.183", "fixed"},
		{"CVE-2026-74580", "6.1.184", "fixed"},
		{"CVE-2026-74580", "6.2.0-rc1", "maybe"},
		{"CVE-2026-74580", "6.2.0", "maybe"},
		{"CVE-2026-74580", "6.6.151", "maybe"},
		{"CVE-2026-74580", "6.6.152-rc1", "maybe"},
		{"CVE-2026-74580", "6.6.152", "fixed"},
		{"CVE-2026-74580", "6.6.153", "fixed"},
		{"CVE-2026-74580", "6.7.0-rc1", "maybe"},
		{"CVE-2026-74580", "6.7.0", "maybe"},
		{"CVE-2026-74580", "6.12.103", "maybe"},
		{"CVE-2026-74580", "6.12.104-pre1", "maybe"},
		{"CVE-2026-74580", "6.12.104-rc1", "maybe"},
		{"CVE-2026-74580", "6.12.104", "fixed"},
		{"CVE-2026-74580", "6.12.105", "fixed"},
		{"CVE-2026-74580", "6.13.0-rc1", "maybe"},
		{"CVE-2026-74580", "6.13.0", "maybe"},
		{"CVE-2026-74580", "6.18.44", "maybe"},
		{"CVE-2026-74580", "6.18.45-rc1", "maybe"},
		{"CVE-2026-74580", "6.18.45", "fixed"},
		{"CVE-2026-74580", "6.18.46", "fixed"},
		{"CVE-2026-74580", "6.19.0-rc1", "maybe"},
		{"CVE-2026-74580", "6.19.0", "maybe"},
		{"CVE-2026-74580", "7.1.8", "maybe"},
		{"CVE-2026-74580", "7.1.9-rc1", "maybe"},
		{"CVE-2026-74580", "7.1.9", "fixed"},
		{"CVE-2026-74580", "7.1.10", "fixed"},
		{"CVE-2026-74580", "7.2.0-pre1", "maybe"},
		{"CVE-2026-74580", "7.2.0-rc1", "maybe"},
		{"CVE-2026-74580", "7.2-rc2", "maybe"},
		{"CVE-2026-74580", "7.2.0-rc3", "maybe"},
		{"CVE-2026-74580", "7.2.0-rc4", "maybe"},
		{"CVE-2026-74580", "7.2-rc4", "maybe"},
		{"CVE-2026-74580", "7.2.0-rc5", "maybe"},
		{"CVE-2026-74580", "7.2.0-rc10", "maybe"},
		{"CVE-2026-74580", "7.2.0-rc4-amd64", "maybe"},
		{"CVE-2026-74580", "7.2.0-rc4+git1", "maybe"},
		{"CVE-2026-74580", "7.2.0", "fixed"},
		{"CVE-2026-74580", "7.2", "fixed"},
		{"CVE-2026-74580", "7.2.0-custom-rc4", "fixed"},
		{"CVE-2026-74580", "7.2.0-1-generic", "fixed"},
		{"CVE-2026-74580", "7.2.0-preempt1", "fixed"},
		{"CVE-2026-74580", "7.2.0-rc4foo", "fixed"},
		{"CVE-2026-74580", "7.2.0-rc", "fixed"},
		{"CVE-2026-74580", "7.2.0-rc-4", "fixed"},
		{"CVE-2026-74580", "7.2.0.1-rc4", "fixed"},
	} {
		bug := kernelCatalogTestBugV1(t, test.id)
		result := matchKernelBugV1(bug, kernelBugFactsV1{Release: test.release})
		if result.Result != test.want {
			t.Fatal(test, result)
		}
	}

	synthetic := kernelBugV1{
		ID:       "CVE-2026-99991",
		Name:     "SyntheticOR",
		URL:      "https://example.invalid/advisory",
		Upstream: []kernelBugRangeV1{{From: "6.1.0", Fixed: "6.1.5"}, {From: "6.6.0-pre0", Fixed: "6.6.2"}},
	}
	if got := matchKernelBugV1(synthetic, kernelBugFactsV1{Release: "6.6.0-rc1"}); got.Result != "maybe" {
		t.Fatal("literal interval OR missed later row", got)
	}
	synthetic.Upstream[1].Fixed = "6.6.2-rc"
	if got := matchKernelBugV1(synthetic, kernelBugFactsV1{Release: "6.6.1"}); got.Result != "unknown" {
		t.Fatal("malformed upstream bound became fixed", got)
	}
}

// These package-format fixtures are synthetic. They do not claim a native
// Debian/Ubuntu receipt or that any real machine is patched.
func TestKernelBugDistributionScopeNeverUsesAnotherFlavourFloor(t *testing.T) {
	for _, test := range []struct{ id, release, version, signature, distro, series, want string }{
		{"CVE-2026-74580", "6.12.100-amd64", "#1 SMP Debian 6.12.100-1 (date)", "", "debian", "13", "affected"},
		{"CVE-2026-74580", "6.12.105-amd64", "#1 SMP Debian 6.12.105-1~bpo13+1 (date)", "", "debian", "13", "affected"},
		{"CVE-2026-74580", "6.12.105-amd64", "#1 SMP Debian 6.12.105-1 (date)", "", "debian", "13", "fixed"},
		{"CVE-2026-74580", "6.12.105-amd64", "#1 SMP Debian 6.12.105-1 (date)", "", "debian", "12", "fixed"},
		{"CVE-2026-74580", "6.12.100-amd64", "#1 SMP Debian 6.12.105-1 (date)", "", "debian", "12", "maybe"},
		{"CVE-2026-74580", "6.18.40-amd64", "#1 SMP Debian 6.18.40-1~bpo13+1 (date)", "", "debian", "13", "maybe"},
		{"CVE-2026-53365", "6.12.99-amd64", "#1 SMP Debian 6.12.99-1 (date)", "", "debian", "13", "affected"},
		{"CVE-2026-53365", "6.12.100-amd64", "#1 SMP Debian 6.12.100-1~bpo13+1 (date)", "", "debian", "13", "affected"},
		{"CVE-2026-53365", "6.12.100-amd64", "#1 SMP Debian 6.12.100-1 (date)", "", "debian", "13", "fixed"},

		// Legitimate Ubuntu 24.04 generic inputs.
		{"CVE-2026-74580", "6.8.0-148-generic", "", "Ubuntu 6.8.0-148.148-generic 6.8.12", "ubuntu", "24.04", "affected"},
		{"CVE-2026-74580", "6.8.0-149-generic", "", "Ubuntu 6.8.0-149.149~24.04.1-generic 6.8.12", "ubuntu", "24.04", "affected"},
		{"CVE-2026-74580", "6.8.0-149-generic", "", "Ubuntu 6.8.0-149.149-generic 6.8.12", "ubuntu", "24.04", "fixed"},
		{"CVE-2026-53365", "6.8.0-146-generic", "", "Ubuntu 6.8.0-146.146-generic 6.8.12", "ubuntu", "24.04", "affected"},
		{"CVE-2026-53365", "6.8.0-147-generic", "", "Ubuntu 6.8.0-147.147-generic 6.8.12", "ubuntu", "24.04", "fixed"},

		// Non-generic flavours, hyphenated *-generic collisions, malformed release
		// ABIs, HWE branches, and other Ubuntu releases fall back to upstream maybe,
		// never false fixed.
		{"CVE-2026-74580", "6.8.0-100-aws-generic", "", "Ubuntu 6.8.0-100.100-aws-generic 6.8.12", "ubuntu", "24.04", "maybe"},
		{"CVE-2026-53365", "6.8.0-100-aws-generic", "", "Ubuntu 6.8.0-100.100-aws-generic 6.8.12", "ubuntu", "24.04", "maybe"},
		{"CVE-2026-74580", "6.8.0-200-gcp-generic", "", "Ubuntu 6.8.0-200.200-gcp-generic 6.8.12", "ubuntu", "24.04", "maybe"},
		{"CVE-2026-74580", "6.8.0-150-generic-64k", "", "Ubuntu 6.8.0-150.150-generic-64k 6.8.12", "ubuntu", "24.04", "maybe"},
		{"CVE-2026-74580", "6.8.0-generic", "", "Ubuntu 6.8.0-150.150-generic 6.8.12", "ubuntu", "24.04", "maybe"},
		{"CVE-2026-74580", "6.8.0-abc-generic", "", "Ubuntu 6.8.0-150.150-generic 6.8.12", "ubuntu", "24.04", "maybe"},
		{"CVE-2026-74580", "6.8.0-060800-generic", "", "Ubuntu 6.8.0-060800.20260101-generic 6.8.0", "ubuntu", "24.04", "maybe"},
		{"CVE-2026-74580", "6.8.0+custom-150-generic", "", "Ubuntu 6.8.0-150.150-generic 6.8.12", "ubuntu", "24.04", "maybe"},
		{"CVE-2026-74580", "6.8.0-1030-aws", "", "Ubuntu 6.8.0-1030.30-aws 6.8.12", "ubuntu", "24.04", "maybe"},
		{"CVE-2026-74580", "6.14.0-37-generic", "", "Ubuntu 6.14.0-37.37-generic 6.14.0", "ubuntu", "24.04", "maybe"},
		{"CVE-2026-74580", "6.8.0-100-oem", "", "Ubuntu 6.8.0-100.100-oem 6.8.12", "ubuntu", "24.04", "maybe"},
		{"CVE-2026-74580", "6.8.0-149-generic", "", "Ubuntu 6.8.0-149.149-generic 6.8.12", "ubuntu", "22.04", "maybe"},

		// Unreadable or inconsistent signature package branch/ABI on an in-scope
		// Ubuntu generic release returns unknown, never false fixed.
		{"CVE-2026-74580", "6.8.0-149-generic", "", "Ubuntu unreadable 6.8.12", "ubuntu", "24.04", "unknown"},
		{"CVE-2026-74580", "6.8.0-148-generic", "", "Ubuntu 6.8.0-149.149-generic 6.8.12", "ubuntu", "24.04", "unknown"},
		{"CVE-2026-74580", "6.8.0-148-generic", "", "Ubuntu 6.14.0-148.148-generic 6.8.12", "ubuntu", "24.04", "unknown"},
		{"CVE-2026-74580", "6.8.0-148-generic", "", "Ubuntu 6.8.0-148.148-generic 6.14.0", "ubuntu", "24.04", "unknown"},
		{"CVE-2026-74580", "6.8.0-148-generic", "", "Ubuntu 6.8.0-100.100-aws-generic 6.8.12", "ubuntu", "24.04", "unknown"},
	} {
		bug := kernelCatalogTestBugV1(t, test.id)
		facts := kernelBugFactsV1{Release: test.release, Version: test.version, Signature: test.signature, Distro: map[string]string{"ID": test.distro, "VERSION_ID": test.series}}
		result := matchKernelBugV1(bug, facts)
		if result.Result != test.want {
			t.Fatal(test, result)
		}
	}

	// Malformed distro package scope rows fall back to upstream maybe rather than false fixed.
	for _, badRow := range []kernelBugDistroV1{
		{ID: "ubuntu", VersionID: "24.04", Flavour: "", Fixed: "6.8.0-149.149"},
		{ID: "ubuntu", VersionID: "24.04", Flavour: "-generic", Fixed: "6.8.0-149.149"},
		{ID: "ubuntu", VersionID: "24.04", Flavour: "generic", Fixed: "6.8.0-149.149-generic"},
		{ID: "ubuntu", VersionID: "24.04", Flavour: "generic", Fixed: "6.8.0-149"},
	} {
		bug := kernelCatalogTestBugV1(t, "CVE-2026-74580")
		bug.Distro = []kernelBugDistroV1{badRow}
		facts := kernelBugFactsV1{
			Release:   "6.8.0-149-generic",
			Signature: "Ubuntu 6.8.0-149.149-generic 6.8.12",
			Distro:    map[string]string{"ID": "ubuntu", "VERSION_ID": "24.04"},
		}
		if got := matchKernelBugV1(bug, facts); got.Result != "maybe" {
			t.Fatal("malformed package scope did not fall back to upstream", badRow, got)
		}
	}
}

func TestKernelMitigationRequiresEveryActualFactAndNeverChangesProbe(t *testing.T) {
	bug := kernelCatalogTestBugV1(t, "CVE-2026-53365")
	facts := kernelBugFactsV1{Release: "6.8.12", ModuleAbsent: map[string]bool{"vhost_vsock": true}}
	if result := matchKernelBugV1(bug, facts); result.Result != "mitigated" || result.Severity != "ok" {
		t.Fatal(result)
	}
	facts.ModuleAbsent = nil
	probe := api.CapProbe{Kernel: "6.8.12", IOURingDisabled: 2, Walls: []api.WallProbe{{Driver: "chv", Level: "microvm", OK: true}}}
	facts.Probe = &probe
	if result := matchKernelBugV1(bug, facts); result.Result != "maybe" {
		t.Fatal("unverified io_uring mitigation marked safe", result)
	}
	if !probe.Walls[0].OK || probe.Walls[0].Level != "microvm" {
		t.Fatal("advisory changed a floor or probe level")
	}
	bug.MitigatedBy = []string{"module:vhost_vsock=absent", "io_uring_disabled=2"}
	facts.ModuleAbsent = map[string]bool{"vhost_vsock": true}
	if result := matchKernelBugV1(bug, facts); result.Result != "mitigated" {
		t.Fatal(result)
	}
	facts.Probe = nil
	if result := matchKernelBugV1(bug, facts); result.Result != "maybe" {
		t.Fatal("unknown probe fact became a mitigation", result)
	}

	vghost := kernelCatalogTestBugV1(t, "CVE-2026-74580")
	vghostFacts := kernelBugFactsV1{
		Release:      "7.2.0-rc1",
		Probe:        &probe,
		ModuleAbsent: map[string]bool{"vhost_vsock": true, "vhost": true, "vhost_net": true},
	}
	if result := matchKernelBugV1(vghost, vghostFacts); result.Result != "maybe" || result.Severity != "warn" {
		t.Fatal("empty mitigation list claimed mitigation", result)
	}
}

func TestDecodeKernelBugCatalogRejectsMalformedBoundsAndScope(t *testing.T) {
	if _, err := loadKernelBugCatalogV1(); err != nil {
		t.Fatal("embedded catalogue failed validation", err)
	}
	const validHeader = `schema = 1
[[bug]]
id = "CVE-2026-90001"
name = "Synthetic"
what = "synthetic test bug"
url = "https://example.com/cve"
checked = 2026-11-20
`
	for _, body := range []string{
		`schema = 2`,
		validHeader + "extra = true\n",
		validHeader + "[[bug.upstream]]\nfrom = \"7.2.0-rc\"\nfixed = \"7.2.0\"\n",
		validHeader + "[[bug.upstream]]\nfrom = \"7.2.0-rc4foo\"\nfixed = \"7.2.0\"\n",
		validHeader + "[[bug.upstream]]\nfrom = \"7.2.0-custom-rc4\"\nfixed = \"7.2.0\"\n",
		validHeader + "[[bug.upstream]]\nfrom = \"7.2\"\nfixed = \"7.2.1\"\n",
		validHeader + "[[bug.upstream]]\nfrom = \"7.1.0-pre0\"\nfixed = \"7.1-rc5\"\n",
		validHeader + "[[bug.upstream]]\nfrom = \"7.2.0-rc0\"\nfixed = \"7.2.0\"\n",
		validHeader + "[[bug.upstream]]\nfrom = \"7.2.0\"\nfixed = \"7.2.0-rc4\"\n",
		validHeader + "[[bug.distro]]\nid = \"ubuntu\"\nversion_id = \"24.04\"\nfixed = \"6.8.0-149.149\"\n",
		validHeader + "[[bug.distro]]\nid = \"ubuntu\"\nversion_id = \"24.04\"\nflavour = \"-generic\"\nfixed = \"6.8.0-149.149\"\n",
		validHeader + "[[bug.distro]]\nid = \"ubuntu\"\nversion_id = \"24.04\"\nflavour = \"generic\"\nfixed = \"6.8.0-149.149-generic\"\n",
		validHeader + "[[bug.distro]]\nid = \"ubuntu\"\nversion_id = \"24.04\"\nflavour = \"generic\"\nfixed = \"6.8.0-149\"\n",
		validHeader + "mitigated_by = [\"unknown_probe_fact=1\"]\n",
		validHeader + "mitigated_by = [\"module:../bad=absent\"]\n",
		validHeader + "mitigated_by = [\"module:vhost_vsock=present\"]\n",
		strings.ReplaceAll(validHeader, "https://example.com/cve", "http://example.com/cve"),
	} {
		if _, err := decodeKernelBugCatalogV1([]byte(body)); err == nil {
			t.Fatal("malformed catalogue accepted:\n" + body)
		}
	}
}
