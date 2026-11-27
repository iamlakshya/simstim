package supply

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
)

// The match rule of isolation spec 20.9: the grammar of a version, then
// dotted numbers, part by part. Each expectation is written by hand.
func TestBelow(t *testing.T) {
	for _, tc := range []struct {
		version, before string
		below           bool
	}{
		// Two parts.
		{"51.1", "51.2", true},
		{"51.2", "51.2", false},
		{"52.0", "51.2", false},
		{"9.9", "51.2", true},
		// Three and four parts, where a string order would be wrong.
		{"6.18.9", "6.18.10", true},
		{"6.18.10", "6.18.9", false},
		{"6.9", "6.18", true},
		{"1.2.3.4", "1.2.3.5", true},
		{"1.2.3.10", "1.2.3.9", false},
		// A missing part counts as 0.
		{"6.18", "6.18.0", false},
		{"6.18.0", "6.18", false},
		{"6.18", "6.18.0.1", true},
		// An optional v.
		{"v54.0", "51.2", false},
		{"v51.1", "v51.2", true},
		// runsc's dated versions: two numbers, with or without release-.
		{"20251103.0", "20251104.0", true},
		{"release-20251103.0", "20251104.0", true},
		{"release-20251103.0", "20251103.0", false},
		{"release-20251103.1", "release-20251103.1", false},
		{"release-20251103.0", "release-20251103.1", true},
		// git describe after a tag comes after it.
		{"0.1.2-45-gabc1234", "0.2.0", true},
		{"0.1.2-45-gabc1234-dirty", "0.2.0", true},
		{"0.2.0-45-gabc1234-dirty", "0.2.0", false},
		{"v0.2.0-1-g3f90e48", "0.2.0", false},
		// A build of a release is that release: the helper's own build
		// (version, a dash and 8 hex digits), and a distribution's.
		{"0.6.0-1a2b3c4d", "0.6.1", true},
		{"0.6.0-1a2b3c4d", "0.6.0", false},
		{"6.18.9-arch1", "6.18.9", false},
		{"6.18.9-arch1-1", "6.18.10", true},
		{"6.18.9-1", "6.18.9", false},
		{"1.3.1", "1.4.0", true},
		// A pre-release of before is below it, and only of before.
		{"v51.2-rc1", "51.2", true},
		{"0.2.0-rc.1", "0.2.0", true},
		{"0.2.0-beta2", "0.2.0", true},
		{"0.2.0-SNAPSHOT-1a2b3c4d", "0.2.0", true},
		{"0.0.0-dev", "0.0.0", true},
		{"0.2.0-rc.1", "0.1.9", false},
		{"0.2.1-alpha", "0.2.0", false},
		// Leading zeros, and numbers too long for an int.
		{"051.1", "51.2", true},
		{"051.02", "51.2", false},
		{"99999999999999999999999.1", "99999999999999999999999.2", true},
		{"99999999999999999999999.1", "9999999999999999999999.9", false},
		// A bare commit, of any shape, with or without -dirty, cannot be
		// read, and fails closed.
		{"1234567", "0.2.0", true},
		{"1234567-dirty", "0.2.0", true},
		{"1234567890abcdef1234567890abcdef12345678", "0.2.0", true},
		{"3c83c21", "0.2.0", true},
		{"3c83c21-dirty", "0.2.0", true},
		{"abc1234", "0.2.0", true},
		{"abc1234-dirty", "0.2.0", true},
		{"c123456-dirty", "54.1", true},
		{"ad97d33", "0.2.0", true},
		{"deadbee", "0.2.0", true},
		// So does every other form outside the grammar.
		{"51", "51.0", true},
		{"1.2.3.4.5", "1.0", true},
		{"main", "51.2", true},
		{"", "51.2", true},
		{"51.2rc1", "51.2", true},
		{"0.2.0~beta2", "0.2.0", true},
		{"0.2.0-dirty", "0.2.0", true},
		{"0.2.0-45-g3f90e48-dirty-1a2b3c4d", "0.2.0", true},
		{"0.2.0+build", "0.2.0", true},
		{"go1.26.11", "1.0", true},
		{"ghostty 1.4.0", "1.4.0", true},
		{"0.2.0\n", "0.2.0", true},
		{" 0.2.0", "0.2.0", true},
		// A before that is not the version of a release fails closed too.
		{"99.0", "", true},
		{"99.0", "next", true},
		{"99.0", "3c83c21", true},
		{"99.0", "51", true},
		{"99.0", "51.2-rc1", true},
		{"99.0", "51.2-1a2b3c4d", true},
	} {
		if got := Below(tc.version, tc.before); got != tc.below {
			t.Errorf("Below(%q, %q) = %v, want %v", tc.version, tc.before, got, tc.below)
		}
	}
}

func TestAdvisoryMatches(t *testing.T) {
	a := Advisory{ID: "GHSA-f47p-p25q-83rh", Kind: AdvisoryVMM, Name: "cloud-hypervisor", Before: "51.2", Severity: "high"}
	for _, tc := range []struct {
		kind, name, version string
		match               bool
	}{
		{AdvisoryVMM, "cloud-hypervisor", "51.1", true},
		{AdvisoryVMM, "cloud-hypervisor", "51.2", false},
		{AdvisoryVMM, "firecracker", "1.0", false},
		{AdvisoryKernel, "cloud-hypervisor", "51.1", false},
		{AdvisoryVMM, "Cloud-Hypervisor", "51.1", false},
	} {
		if got := a.Matches(tc.kind, tc.name, tc.version); got != tc.match {
			t.Errorf("Matches(%s, %s, %s) = %v", tc.kind, tc.name, tc.version, got)
		}
	}
}

func TestAdvisoryKind(t *testing.T) {
	for item, want := range map[string]string{
		KindVMM: AdvisoryVMM, KindKernel: AdvisoryKernel, KindGvisor: AdvisoryGvisor, KindVT: AdvisoryTerminal,
		KindHelper: "", KindTools: "", KindImage: "", KindUserns: "", KindLima: "",
	} {
		if got := AdvisoryKind(item); got != want {
			t.Errorf("AdvisoryKind(%s) = %q, want %q", item, got, want)
		}
	}
}

var hexD = strings.Repeat("d", 64)

// bad is a release manifest whose runtime matches its own list: a VMM of
// 51.1 on both architectures, and a shadow terminal below its entry.
func bad() *Manifest {
	m := sample()
	vmm := m.Items[2]
	vmm.Version = "51.1"
	arm := vmm
	arm.Arch, arm.SHA256 = "arm64", hexD
	m.Items[2] = vmm
	m.Items = append(m.Items, arm, NewItem(KindVT, brand.VTBinary, "0.1.3", "amd64", []byte("VT"), base+"vt-linux-amd64"))
	m.Advisories = append(m.Advisories,
		Advisory{ID: "GHSA-w9q4-5jvv-mcv4", Kind: AdvisoryTerminal, Name: brand.VTBinary, Before: "0.2.0", Severity: "medium", CVSS: 6.5})
	return m
}

func TestOwnFindings(t *testing.T) {
	if got := sample().OwnFindings(); len(got) != 0 {
		t.Errorf("a release clear of its list: %v", got)
	}
	m := bad()
	if _, err := Load(raw(t, m)); err != nil {
		t.Fatalf("Load refused a release that matches its own list, which the client must still read: %v", err)
	}
	var got []string
	for _, f := range m.OwnFindings() {
		got = append(got, f.String())
	}
	want := []string{"cloud-hypervisor 51.1 matches GHSA-f47p-p25q-83rh", brand.VTBinary + " 0.1.3 matches GHSA-w9q4-5jvv-mcv4"}
	if !slices.Equal(got, want) {
		t.Errorf("findings = %q, want %q (one for the VMM of both architectures)", got, want)
	}
	// The helper carries the shadow terminal, but only the vt item counts.
	m.Items[0].Version = "0.0.1"
	if n := len(m.OwnFindings()); n != 2 {
		t.Errorf("an old helper item gave %d findings, want 2", n)
	}
}

func TestRuntimeFinding(t *testing.T) {
	m := bad()
	for _, digests := range [][]string{{strings.Repeat("c", 64), hexA}, {hexD}, {hexB, hexD}} {
		f, ok := m.RuntimeFinding(digests)
		if !ok || f.Item.Name != "cloud-hypervisor" || f.Item.Version != "51.1" || f.Advisory.ID != "GHSA-f47p-p25q-83rh" {
			t.Errorf("a start with the VMM of 51.1, by %d digests: %+v, %v", len(digests), f, ok)
		}
	}
	for name, digests := range map[string][]string{
		"no runtime":          nil,
		"a digest of no item": {strings.Repeat("c", 64)},
		"the shadow terminal": {m.Items[len(m.Items)-1].SHA256},
		"the helper":          {m.Items[0].SHA256},
		"a clear kernel":      {hexB},
	} {
		if f, ok := m.RuntimeFinding(digests); ok {
			t.Errorf("%s: %+v", name, f)
		}
	}
	if f, ok := sample().RuntimeFinding([]string{hexA}); ok {
		t.Errorf("the VMM of 54.1: %+v", f)
	}
}

func TestUnheld(t *testing.T) {
	m := sample()
	if d, ok := m.Unheld([]string{hexA, hexB}); ok {
		t.Errorf("digests that the manifest holds: %q", d)
	}
	if d, ok := m.Unheld(nil); ok {
		t.Errorf("no digest: %q", d)
	}
	for _, digests := range [][]string{{hexA, hexD}, {hexD}, {""}, {hexA, ""}} {
		if d, ok := m.Unheld(digests); !ok || d != digests[len(digests)-1] {
			t.Errorf("Unheld(%q) = %q, %v", digests, d, ok)
		}
	}
}

func TestPinAges(t *testing.T) {
	// The local zone must not change the day: it is UTC's.
	oldLocal := time.Local
	time.Local = time.FixedZone("UTC-11", -11*3600)
	t.Cleanup(func() { time.Local = oldLocal })

	m := sample()
	// v54.1 is of 11 November 2026.
	now := time.Date(2026, 12, 8, 15, 4, 5, 0, time.UTC)
	if got, want := m.PinAges("amd64", now), []string{"cloud-hypervisor 54.1: 27 days old, fixes until 2027-01-06"}; !slices.Equal(got, want) {
		t.Errorf("amd64 = %q, want %q", got, want)
	}
	if got, want := m.PinAges("arm64", now), []string{"guest 6.18: 71 days old"}; !slices.Equal(got, want) {
		t.Errorf("arm64 = %q, want %q", got, want)
	}
	if got := m.PinAges("riscv64", now); len(got) != 0 {
		t.Errorf("an architecture with no pins = %q", got)
	}
	// Only a VMM, a guest kernel or runsc is a runtime pin, though another
	// item may have a date.
	m.Items[5].Released, m.Items[6].Released = "2026-10-01", "2026-10-02"
	if got := m.PinAges("arm64", now); !slices.Equal(got, []string{"guest 6.18: 71 days old"}) {
		t.Errorf("arm64 with a dated lima and vm-image = %q", got)
	}
	for _, tc := range []struct {
		now  time.Time
		want string
	}{
		{time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC), "cloud-hypervisor 54.1: 1 day old, fixes until 2027-01-06"},
		{time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), "cloud-hypervisor 54.1: 0 days old, fixes until 2027-01-06"},
		{time.Date(2027, 1, 6, 23, 0, 0, 0, time.UTC), "cloud-hypervisor 54.1: 56 days old, fixes until 2027-01-06"},
		{time.Date(2027, 1, 7, 1, 0, 0, 0, time.UTC), "cloud-hypervisor 54.1: 57 days old, fixes ended 2027-01-06"},
		// The same moments in zones far from UTC, on the other side of the day.
		{time.Date(2027, 1, 7, 13, 0, 0, 0, time.FixedZone("UTC+14", 14*3600)), "cloud-hypervisor 54.1: 56 days old, fixes until 2027-01-06"},
		{time.Date(2027, 1, 6, 15, 0, 0, 0, time.FixedZone("UTC-10", -10*3600)), "cloud-hypervisor 54.1: 57 days old, fixes ended 2027-01-06"},
		{time.Date(2027, 1, 7, 1, 0, 0, 0, time.FixedZone("UTC+2", 2*3600)), "cloud-hypervisor 54.1: 56 days old, fixes until 2027-01-06"},
	} {
		if got := m.PinAges("amd64", tc.now); len(got) != 1 || got[0] != tc.want {
			t.Errorf("at %s: %q, want %q", tc.now, got, tc.want)
		}
	}
}

// The pin and the advisory floor of Cloud Hypervisor (spec 20.3).
const chvPin, chvFloor = "54.1", "54.1"

// The list that each release carries is valid, and it covers what the
// spec says (20.3, 20.9 and 19.1).
func TestTheKnownBadList(t *testing.T) {
	m := sample()
	m.Advisories = KnownBad()
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		kind, name, version string
		match               bool
	}{
		{AdvisoryVMM, "cloud-hypervisor", "50.0", true},
		{AdvisoryVMM, "cloud-hypervisor", "51.1", true},
		{AdvisoryVMM, "cloud-hypervisor", "52.0", true},  // GHSA-3r3x-6hgg-2m38
		{AdvisoryVMM, "cloud-hypervisor", "53.1", true},  // fixed, but one before cannot say so: fail closed
		{AdvisoryVMM, "cloud-hypervisor", "54.0", true},  // GHSA-3r3x-6hgg-2m38
		{AdvisoryVMM, "cloud-hypervisor", "54.1", false}, // the pin of 20.3
		{AdvisoryTerminal, brand.VTBinary, "0.1.2", true},
		{AdvisoryTerminal, brand.VTBinary, "0.2.0", true}, // unresolved
		{AdvisoryTerminal, brand.VTBinary, "1.0.0", true}, // unresolved
		{AdvisoryLaptopTerminal, "ghostty", "1.3.1", true},
		{AdvisoryLaptopTerminal, "ghostty", "1.4.0", false},
	} {
		if got := len(m.Matches(tc.kind, tc.name, tc.version)) > 0; got != tc.match {
			t.Errorf("%s %s %s: match = %v", tc.kind, tc.name, tc.version, got)
		}
	}
	// The pin and the floor are clear of every VMM entry, and so is each
	// VMM that guest/supply.lock pins.
	vmms := append([]string{chvPin, chvFloor}, lockedVMMs(t)...)
	for _, v := range vmms {
		if found := m.Matches(AdvisoryVMM, "cloud-hypervisor", v); len(found) > 0 {
			t.Errorf("the pinned cloud-hypervisor %s is on the list: %+v", v, found)
		}
	}
	// KnownBad hands out a copy.
	KnownBad()[0].Before = "0"
	if knownBad[0].Before == "0" {
		t.Error("KnownBad shares its list")
	}
}

// lockedVMMs reads the version of each cloud-hypervisor [[vmm]] table of
// guest/supply.lock (spec 20.1). P3.1 adds the table.
func lockedVMMs(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile("../../guest/supply.lock")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, table := range strings.Split(string(b), "[[vmm]]")[1:] {
		table, _, _ = strings.Cut(table, "\n[")
		if !strings.Contains(table, `name = "cloud-hypervisor"`) {
			continue
		}
		if m := lockVersion.FindStringSubmatch(table); m != nil {
			out = append(out, m[1])
		}
	}
	return out
}

var lockVersion = regexp.MustCompile(`(?m)^version = "([^"]+)"`)

// MergeAdvisories keeps one entry for each ID, kind and name: the stronger
// one, or the newer list's when they are as strong.
func TestMergeAdvisories(t *testing.T) {
	base := Advisory{ID: "GHSA-3r3x-6hgg-2m38", Kind: AdvisoryVMM, Name: "cloud-hypervisor", Before: "54.1", Severity: "high", CVSS: 8.9}
	with := func(change func(a *Advisory)) Advisory {
		a := base
		change(&a)
		return a
	}
	lower := with(func(a *Advisory) { a.Before = "52.0" })
	higher := with(func(a *Advisory) { a.Before = "54.10" })
	open := with(func(a *Advisory) { a.Before, a.Unresolved = "", true })
	reworded := with(func(a *Advisory) { a.Severity, a.CVSS = "critical", 0 })
	other := with(func(a *Advisory) { a.Kind, a.Name, a.Before = AdvisoryLaptopTerminal, "ghostty", "1.4.0" })
	for name, tc := range map[string]struct {
		have, add, want []Advisory
	}{
		"a new entry":                 {nil, []Advisory{base}, []Advisory{base}},
		"a weaker boundary in have":   {[]Advisory{lower}, []Advisory{base}, []Advisory{base}},
		"a stronger boundary in have": {[]Advisory{higher}, []Advisory{base}, []Advisory{higher}},
		"a weaker boundary in add":    {[]Advisory{base}, []Advisory{lower}, []Advisory{base}},
		"unresolved in have":          {[]Advisory{open}, []Advisory{higher}, []Advisory{open}},
		"unresolved in add":           {[]Advisory{higher}, []Advisory{open}, []Advisory{open}},
		"as strong: add's wins":       {[]Advisory{base}, []Advisory{reworded}, []Advisory{reworded}},
		"another kind stays apart":    {[]Advisory{base}, []Advisory{other}, []Advisory{base, other}},
	} {
		got := MergeAdvisories(tc.have, tc.add)
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s: %+v, want %+v", name, got, tc.want)
		}
		if err := ValidateAdvisories(got); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Unresolved beats any before, also one that an entry still carries,
	// as Matches reads it: an unresolved entry covers every version.
	stale := with(func(a *Advisory) { a.Unresolved = true })
	if got := MergeAdvisories([]Advisory{stale}, []Advisory{higher}); !slices.Equal(got, []Advisory{stale}) {
		t.Errorf("unresolved with a before: %+v", got)
	}
}

// An unresolved entry covers every version, until a release names itself
// as the first one with the fix.
func TestUnresolved(t *testing.T) {
	m := sample()
	m.Advisories = []Advisory{{ID: "GHSA-w9q4-5jvv-mcv4", Kind: AdvisoryTerminal, Name: brand.VTBinary, Unresolved: true, Severity: "medium"}}
	if _, err := Load(raw(t, m)); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"0.1.2", "0.2.0", "0.3.0", "1.0.0", "main"} {
		if !m.Advisories[0].Matches(AdvisoryTerminal, brand.VTBinary, v) {
			t.Errorf("unresolved: %s is clear", v)
		}
	}
	vt := NewItem(KindVT, brand.VTBinary, "0.2.0", "amd64", []byte("VT"), base+"vt-linux-amd64")
	m.Items = append(m.Items, vt)
	if got := m.OwnFindings(); len(got) != 1 || got[0].String() != brand.VTBinary+" 0.2.0 matches GHSA-w9q4-5jvv-mcv4, whose first fixed release is unresolved" {
		t.Errorf("findings = %v", got)
	}
	m.Advisories[0].Unresolved, m.Advisories[0].Before = false, "0.2.0"
	for v, match := range map[string]bool{"0.1.9": true, "0.2.0-rc.1": true, "0.2.0": false, "0.3.0": false, "1.0.0": false} {
		if got := m.Advisories[0].Matches(AdvisoryTerminal, brand.VTBinary, v); got != match {
			t.Errorf("resolved at 0.2.0: %s match = %v", v, got)
		}
	}
}

func TestLoadRefusesBadAdvisories(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		change     func(m *Manifest)
	}{
		{"no id", "no id", func(m *Manifest) { m.Advisories[0].ID = "" }},
		{"no name", "no name", func(m *Manifest) { m.Advisories[0].Name = "" }},
		{"before with no digit", `before "next" is not the version of a release`, func(m *Manifest) { m.Advisories[0].Before = "next" }},
		{"before as a bare commit", `before "3c83c21" is not the version`, func(m *Manifest) { m.Advisories[0].Before = "3c83c21" }},
		{"before as a pre-release", `before "51.2-rc1" is not the version`, func(m *Manifest) { m.Advisories[0].Before = "51.2-rc1" }},
		{"before as one number", `before "51" is not the version`, func(m *Manifest) { m.Advisories[0].Before = "51" }},
		{"before as a git describe version", `is not the version`, func(m *Manifest) { m.Advisories[0].Before = "51.2-3-gabc1234" }},
		{"before as a build", `is not the version`, func(m *Manifest) { m.Advisories[0].Before = "51.2-1" }},
		{"no before", "no before, and not unresolved", func(m *Manifest) { m.Advisories[0].Before = "" }},
		{"unresolved with a before", `an unresolved entry has no before, not "51.2"`, func(m *Manifest) { m.Advisories[0].Unresolved = true }},
		{"no severity", "is not one lower-case word", func(m *Manifest) { m.Advisories[0].Severity = "" }},
		{"upper-case severity", "is not one lower-case word", func(m *Manifest) { m.Advisories[0].Severity = "High" }},
		{"a CVSS over 10", "not between 0 and 10", func(m *Manifest) { m.Advisories[0].CVSS = 10.1 }},
		{"a negative CVSS", "not between 0 and 10", func(m *Manifest) { m.Advisories[0].CVSS = -1 }},
		{"a high CVSS with another word", "makes it high", func(m *Manifest) {
			m.Advisories[0].CVSS, m.Advisories[0].Severity = 7.0, "medium"
		}},
		{"a critical CVSS", "makes it high", func(m *Manifest) { m.Advisories[0].CVSS, m.Advisories[0].Severity = 9.8, "critical" }},
		{"a published date that does not exist", "is not a date", func(m *Manifest) { m.Advisories[0].Published = "2026-02-30" }},
		{"an http url", "not an https URL", func(m *Manifest) { m.Advisories[0].URL = "http://example.org/GHSA" }},
		{"a terminal entry of another program", "names the shadow terminal", func(m *Manifest) {
			m.Advisories[0].Kind, m.Advisories[0].Name = AdvisoryTerminal, "ghostty"
		}},
		{"a laptop terminal by its full answer", "in lower case", func(m *Manifest) {
			m.Advisories[0].Kind, m.Advisories[0].Name = AdvisoryLaptopTerminal, "iTerm2"
		}},
		{"an entry twice", "twice", func(m *Manifest) { m.Advisories = append(m.Advisories, m.Advisories[0]) }},
		{"a vmm with no released date", "needs its released date", func(m *Manifest) { m.Items[2].Released = "" }},
		{"a kernel with a released date that does not exist", "needs its released date", func(m *Manifest) { m.Items[3].Released = "2026-13-01" }},
		{"a gvisor item with no released date", "needs its released date", func(m *Manifest) {
			m.Items = append(m.Items, NewItem(KindGvisor, "runsc", "release-20261102.0", "amd64", []byte("RUNSC"), base+"runsc-amd64"))
		}},
		{"a helper with a released date that is not a date", "is not a date", func(m *Manifest) { m.Items[0].Released = "yesterday" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := sample()
			tc.change(m)
			if _, err := Load(raw(t, m)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Load: err = %v, want %q", err, tc.want)
			}
		})
	}
	// The same advisory may name the shadow terminal and a laptop's terminal.
	m := sample()
	a := Advisory{ID: "GHSA-w9q4-5jvv-mcv4", Kind: AdvisoryTerminal, Name: brand.VTBinary, Before: "0.2.0", Severity: "medium"}
	b := a
	b.Kind, b.Name, b.Before = AdvisoryLaptopTerminal, "ghostty", "1.4.0"
	m.Advisories = append(m.Advisories, a, b)
	if _, err := Load(raw(t, m)); err != nil {
		t.Errorf("one advisory for two kinds: %v", err)
	}
}
