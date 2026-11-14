package supply

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
)

// The kinds of advisory (isolation spec 20.9). An advisory of kind vmm,
// kernel or gvisor covers the items of that kind. A terminal advisory
// covers the shadow terminal, by the helper version that carries it. A
// laptop_terminal advisory covers a terminal program on the laptop, by the
// first dotted number of its XTVERSION answer.
const (
	AdvisoryVMM            = KindVMM
	AdvisoryKernel         = KindKernel
	AdvisoryGvisor         = KindGvisor
	AdvisoryTerminal       = "terminal"
	AdvisoryLaptopTerminal = "laptop_terminal"
)

var advisoryKinds = map[string]bool{
	AdvisoryVMM: true, AdvisoryKernel: true, AdvisoryGvisor: true, AdvisoryTerminal: true, AdvisoryLaptopTerminal: true,
}

// pinKinds are the kinds of item that pin an upstream release. Each has
// its released date, and the advisories of its own kind cover it.
var pinKinds = map[string]bool{KindVMM: true, KindKernel: true, KindGvisor: true}

var letters = regexp.MustCompile(`^[a-z]+$`)

// AdvisoryKind is the kind of advisory that covers an item of a kind, or ""
// when no advisory covers it. The shadow terminal's item (vt) counts as
// terminal: a terminal advisory names it, and its version is the release's.
func AdvisoryKind(itemKind string) string {
	if itemKind == KindVT {
		return AdvisoryTerminal
	}
	if pinKinds[itemKind] {
		return itemKind
	}
	return ""
}

// Matches reports whether a covers something of a kind of advisory, a
// name and a version: the kind and the name are equal, and the version is
// below a.Before (isolation spec 20.9). An unresolved entry covers every
// version.
func (a Advisory) Matches(kind, name, version string) bool {
	return a.Kind == kind && a.Name == name && (a.Unresolved || Below(version, a.Before))
}

// Matches lists the advisories of m that cover something of a kind of
// advisory, a name and a version, in the list's order.
func (m *Manifest) Matches(kind, name, version string) []Advisory {
	var found []Advisory
	for _, a := range m.Advisories {
		if a.Matches(kind, name, version) {
			found = append(found, a)
		}
	}
	return found
}

// Finding is an item of a release manifest that an advisory covers.
type Finding struct {
	Item     Item
	Advisory Advisory
}

func (f Finding) String() string {
	s := fmt.Sprintf("%s %s matches %s", f.Item.Name, f.Item.Version, f.Advisory.ID)
	if f.Advisory.Unresolved {
		s += ", whose first fixed release is unresolved"
	}
	return s
}

// OwnFindings lists the items of m that its own advisories cover. A release
// must have none (isolation spec 20.9): the release job fails on any. An
// item on two architectures counts once.
func (m *Manifest) OwnFindings() []Finding {
	var found []Finding
	seen := map[string]bool{}
	for _, it := range m.Items {
		kind := AdvisoryKind(it.Kind)
		if kind == "" {
			continue
		}
		for _, a := range m.Matches(kind, it.Name, it.Version) {
			key := strings.Join([]string{it.Kind, it.Name, it.Version, a.ID}, " ")
			if !seen[key] {
				seen[key] = true
				found = append(found, Finding{it, a})
			}
		}
	}
	return found
}

// Unheld returns the first digest that no item of m holds, or false when m
// holds them all. A start names its runtime by the digests of the client's
// release manifest, so such a digest is a runtime that the client cannot
// check against its list, such as an older client's.
func (m *Manifest) Unheld(digests []string) (string, bool) {
	for _, d := range digests {
		if !slices.ContainsFunc(m.Items, func(it Item) bool { return d != "" && it.SHA256 == d }) {
			return d, true
		}
	}
	return "", false
}

// RuntimeFinding finds the first runtime file of a start, named by its
// SHA-256 in m, that an advisory of m covers (isolation spec 20.9). It
// skips a digest that m does not hold: Unheld finds those.
func (m *Manifest) RuntimeFinding(digests []string) (Finding, bool) {
	for _, d := range digests {
		for _, it := range m.Items {
			if d == "" || it.SHA256 != d || !pinKinds[it.Kind] {
				continue
			}
			if found := m.Matches(it.Kind, it.Name, it.Version); len(found) > 0 {
				return Finding{it, found[0]}, true
			}
		}
	}
	return Finding{}, false
}

// PinAges describes the age of each runtime pin of m, a VMM, a guest
// kernel or runsc, for a host of arch, on the UTC day of now (isolation
// spec 20.9): "cloud-hypervisor 54.1: 27 days old, fixes until
// 2027-01-06". It reads each item's released date, and a VMM's eol.
func (m *Manifest) PinAges(arch string, now time.Time) []string {
	today := now.UTC().Format(time.DateOnly)
	var lines []string
	for _, it := range m.Items {
		if it.Arch != arch || !pinKinds[it.Kind] || it.Released == "" {
			continue
		}
		line := fmt.Sprintf("%s %s: %s", it.Name, it.Version, age(it.Released, now))
		switch {
		case it.EOL == "":
		case it.EOL >= today:
			line += ", fixes until " + it.EOL
		default:
			line += ", fixes ended " + it.EOL
		}
		lines = append(lines, line)
	}
	return lines
}

// age says how old a release of the date released is, in whole days.
func age(released string, now time.Time) string {
	r, err := time.Parse(time.DateOnly, released)
	if err != nil {
		return "released " + released
	}
	days := max(int(now.UTC().Sub(r).Hours()/24), 0)
	if days == 1 {
		return "1 day old"
	}
	return fmt.Sprintf("%d days old", days)
}

// MergeAdvisories returns have with each entry of add. An entry that both
// hold, by ID, kind and name, appears once: the stronger of the two, by the
// rule of stronger, or add's when they are as strong, because add is the
// newer list. Entries of one ID for different kinds stay apart.
func MergeAdvisories(have, add []Advisory) []Advisory {
	out := slices.Clone(have)
	for _, a := range add {
		i := slices.IndexFunc(out, func(b Advisory) bool { return b.ID == a.ID && b.Kind == a.Kind && b.Name == a.Name })
		switch {
		case i < 0:
			out = append(out, a)
		case !stronger(out[i], a):
			out[i] = a
		}
	}
	return out
}

// ValidateAdvisories checks a known-bad list, as Load does.
func ValidateAdvisories(list []Advisory) error { return validateAdvisories(list) }

// validateAdvisories checks the known-bad list of a manifest.
func validateAdvisories(list []Advisory) error {
	seen := map[string]bool{}
	for _, a := range list {
		if err := a.validate(); err != nil {
			return fmt.Errorf("the release manifest's advisory %q: %w", a.ID, err)
		}
		key := a.ID + " " + a.Kind + " " + a.Name
		if seen[key] {
			return fmt.Errorf("the release manifest names advisory %s for %s %s twice", a.ID, a.Kind, a.Name)
		}
		seen[key] = true
	}
	return nil
}

func (a Advisory) validate() error {
	switch {
	case a.ID == "":
		return errors.New("no id")
	case !advisoryKinds[a.Kind]:
		return fmt.Errorf("unknown kind %q", a.Kind)
	case a.Name == "":
		return errors.New("no name")
	case a.Unresolved && a.Before != "":
		return fmt.Errorf("an unresolved entry has no before, not %q", a.Before)
	case !a.Unresolved && a.Before == "":
		return errors.New("no before, and not unresolved")
	case !a.Unresolved && !cleanVersion(a.Before):
		return fmt.Errorf("before %q is not the version of a release, such as 51.2", a.Before)
	case a.Kind == AdvisoryTerminal && a.Name != brand.VTBinary:
		return fmt.Errorf("a terminal advisory names the shadow terminal, %s, not %q", brand.VTBinary, a.Name)
	case a.Kind == AdvisoryLaptopTerminal && !letters.MatchString(a.Name):
		return fmt.Errorf("a laptop_terminal advisory names a program by the letters that start its XTVERSION answer, in lower case, not %q", a.Name)
	case !letters.MatchString(a.Severity):
		return fmt.Errorf("severity %q is not one lower-case word, such as high", a.Severity)
	case a.CVSS < 0 || a.CVSS > 10:
		return fmt.Errorf("cvss %v is not between 0 and 10", a.CVSS)
	case a.CVSS >= 7 && a.Severity != "high":
		return fmt.Errorf("severity is %q, but a CVSS of %v makes it high", a.Severity, a.CVSS)
	case a.Published != "" && !isDate(a.Published):
		return fmt.Errorf("published %q is not a date as YYYY-MM-DD", a.Published)
	}
	if a.URL != "" {
		if err := checkURL(a.URL); err != nil {
			return fmt.Errorf("url: %w", err)
		}
	}
	return nil
}
