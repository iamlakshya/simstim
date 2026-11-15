package supply

import (
	"slices"

	"github.com/iamlakshya/simstim/internal/brand"
)

// knownBad is the known-bad list that each release manifest carries
// (isolation spec 20.9). Before is the lowest version from which every
// later version has the fix. A release ships each new entry within 7 days
// of the advisory's publication, for an advisory of CVSS 7.0 or more and
// for one whose range holds a pin of the release (docs/contributing/
// releasing.md, question 42).
//
// One Before cannot name two fixed branches. GHSA-3r3x-6hgg-2m38 is fixed
// in 53.1 and in 54.1, and its Before of 54.1 also marks 53.1 as bad. That
// fails closed, and the pin is 54.1 (spec 20.3).
//
// The shadow terminal's entries are unresolved: no release yet has a
// helper whose Ghostty pin holds every fix. While they are, the release job
// refuses every release. The release that carries the fixed pin (vt-1.0.0,
// P0.1) sets Before to its own version and drops Unresolved.
//
// Left out, and why: GHSA-g6mw-f26h-4jgp of Cloud Hypervisor names only
// v30.0 and v31.0, below every pin. The other Ghostty advisories name app
// versions that the laptop_terminal entry's Before of 1.4.0 already covers,
// or library fixes that the shadow terminal's pin already holds (spec
// 19.1). The Firecracker entries also conservatively cover fixed older
// branches: the driver floor is 1.17.0 and the release pin is 1.18.0. A guest kernel
// advisory is added when it covers the current kernel pin.
var knownBad = []Advisory{
	{ID: "GHSA-48ww-rqpp-2mgm", Kind: AdvisoryVMM, Name: FirecrackerName, Before: "1.17.0", Severity: "high", CVSS: 7.5, Published: "2026-11-04",
		URL: "https://github.com/firecracker-microvm/firecracker/security/advisories/GHSA-48ww-rqpp-2mgm"},
	{ID: "GHSA-776c-mpj7-jm3r", Kind: AdvisoryVMM, Name: FirecrackerName, Before: "1.15.1", Severity: "high", Published: "2026-04-07",
		URL: "https://github.com/firecracker-microvm/firecracker/security/advisories/GHSA-776c-mpj7-jm3r"},
	{ID: "GHSA-3r3x-6hgg-2m38", Kind: AdvisoryVMM, Name: "cloud-hypervisor", Before: "54.1", Severity: "high", CVSS: 8.9, Published: "2026-11-11",
		URL: "https://github.com/cloud-hypervisor/cloud-hypervisor/security/advisories/GHSA-3r3x-6hgg-2m38"},
	{ID: "GHSA-f47p-p25q-83rh", Kind: AdvisoryVMM, Name: "cloud-hypervisor", Before: "51.2", Severity: "high", CVSS: 8.9, Published: "2026-05-14",
		URL: "https://github.com/cloud-hypervisor/cloud-hypervisor/security/advisories/GHSA-f47p-p25q-83rh"},
	{ID: "GHSA-jmr4-g2hv-mjj6", Kind: AdvisoryVMM, Name: "cloud-hypervisor", Before: "50.1", Severity: "high", CVSS: 7.2, Published: "2026-02-20",
		URL: "https://github.com/cloud-hypervisor/cloud-hypervisor/security/advisories/GHSA-jmr4-g2hv-mjj6"},
	{ID: "GHSA-w9q4-5jvv-mcv4", Kind: AdvisoryTerminal, Name: brand.VTBinary, Unresolved: true, Severity: "medium", CVSS: 6.5, Published: "2026-10-27",
		URL: "https://github.com/ghostty-org/ghostty/security/advisories/GHSA-w9q4-5jvv-mcv4"},
	{ID: "GHSA-jqr7-69mm-v35h", Kind: AdvisoryTerminal, Name: brand.VTBinary, Unresolved: true, Severity: "medium", CVSS: 5.5, Published: "2026-10-27",
		URL: "https://github.com/ghostty-org/ghostty/security/advisories/GHSA-jqr7-69mm-v35h"},
	{ID: "GHSA-w9q4-5jvv-mcv4", Kind: AdvisoryLaptopTerminal, Name: "ghostty", Before: "1.4.0", Severity: "medium", CVSS: 6.5, Published: "2026-10-27",
		URL: "https://github.com/ghostty-org/ghostty/security/advisories/GHSA-w9q4-5jvv-mcv4"},
}

// KnownBad returns a copy of the known-bad list. cmd/supply write puts it
// in each release manifest. The client reads the list in its release
// manifest, and merges this list into it (MergeAdvisories), so a build with
// an old manifest file, or none, never gets a weaker list.
func KnownBad() []Advisory { return slices.Clone(knownBad) }
