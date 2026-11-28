package host

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	toml "github.com/pelletier/go-toml/v2"

	"github.com/iamlakshya/simstim/internal/api"
)

//go:embed kernel_bugs.toml
var embeddedKernelBugsTOML []byte

// KernelBugList is the decoded schema-1 embedded host kernel advisory list
// (isolation spec 20.11).
type KernelBugList struct {
	Schema int         `toml:"schema"`
	Bugs   []KernelBug `toml:"bug"`
}

// KernelBug describes one known host kernel local root vulnerability.
type KernelBug struct {
	ID          string            `toml:"id"`
	Name        string            `toml:"name"`
	What        string            `toml:"what"`
	URL         string            `toml:"url"`
	Checked     string            `toml:"checked"`
	MitigatedBy []string          `toml:"mitigated_by"`
	RootStep    string            `toml:"root_step"`
	Upstream    []KernelBugRange  `toml:"upstream"`
	Distro      []KernelBugDistro `toml:"distro"`
}

// KernelBugRange describes one upstream stable line affected by a bug.
type KernelBugRange struct {
	From  string `toml:"from"`
	Fixed string `toml:"fixed"`
}

// KernelBugDistro describes one distribution's fixed kernel package version.
type KernelBugDistro struct {
	ID        string `toml:"id"`
	VersionID string `toml:"version_id"`
	Series    string `toml:"series"`
	Fixed     string `toml:"fixed"`
}

// KernelBugEnv holds the host kernel and distro inputs read by KernelBugChecks.
// Unit tests construct it directly with recorded fixture strings on any OS.
type KernelBugEnv struct {
	GOOS             string
	UnameR           string
	UnameV           string
	VersionSignature string
	OSRelease        []byte
	ModuleLoaded     func(name string) bool
	Facts            map[string]string
}

// ParseKernelBugList strictly decodes a kernel_bugs.toml document.
func ParseKernelBugList(data []byte) (KernelBugList, error) {
	var list KernelBugList
	dec := toml.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&list); err != nil {
		return KernelBugList{}, fmt.Errorf("decode kernel_bugs.toml: %w", err)
	}
	if list.Schema != 1 {
		return KernelBugList{}, fmt.Errorf("unsupported kernel_bugs.toml schema %d", list.Schema)
	}
	return list, nil
}

// EmbeddedKernelBugList parses the host helper's embedded kernel_bugs.toml.
func EmbeddedKernelBugList() (KernelBugList, error) {
	return ParseKernelBugList(embeddedKernelBugsTOML)
}

// KernelBugChecks evaluates the running host kernel against the embedded list
// of known local root bugs for host doctor (isolation spec 20.11).
// It never alters a host's wall floor or refuses a start (question 7).
func KernelBugChecks() ([]Check, []api.KernelBugResult) {
	if runtime.GOOS != "linux" {
		return nil, nil
	}
	list, err := EmbeddedKernelBugList()
	if err != nil {
		return nil, nil
	}
	env := collectKernelBugEnv()
	return EvaluateKernelBugs(list, env)
}

func collectKernelBugEnv() KernelBugEnv {
	env := KernelBugEnv{
		GOOS: runtime.GOOS,
		ModuleLoaded: func(name string) bool {
			if name == "" || strings.ContainsAny(name, "/\\.") {
				return false
			}
			_, err := os.Stat(filepath.Join("/sys/module", name))
			return err == nil
		},
		Facts: map[string]string{},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "uname", "-r").Output(); err == nil {
		env.UnameR = strings.TrimSpace(string(out))
	}
	if out, err := exec.CommandContext(ctx, "uname", "-v").Output(); err == nil {
		env.UnameV = strings.TrimSpace(string(out))
	}
	if b, err := os.ReadFile("/proc/version_signature"); err == nil {
		env.VersionSignature = strings.TrimSpace(string(b))
	}
	if b, err := os.ReadFile("/etc/os-release"); err == nil {
		env.OSRelease = b
	}
	if v := readProcInt(filepath.Join(procRoot, "sys/kernel/io_uring_disabled"), -1); v >= 0 {
		env.Facts["io_uring_disabled"] = strconv.Itoa(v)
	}
	return env
}

// EvaluateKernelBugs checks env against list using the match rules of
// isolation spec 20.11 (distribution package first, upstream triple fallback,
// advisory mitigations, and unknown-version handling).
func EvaluateKernelBugs(list KernelBugList, env KernelBugEnv) ([]Check, []api.KernelBugResult) {
	if env.GOOS != "" && env.GOOS != "linux" {
		return nil, nil
	}
	distroID, versionID := ParseOSReleaseIDVersion(env.OSRelease)
	pkgVer := extractDistroPkgVersion(distroID, env.UnameV, env.VersionSignature)
	upVer, hasUp := parseKernelTriple(env.UnameR)

	if pkgVer == "" && !hasUp {
		const msg = "The kernel's version could not be read, so the list of known bugs was not checked."
		res := []api.KernelBugResult{{
			ID:       "kernel-version",
			Status:   api.KernelBugUnknown,
			Severity: "ok",
			Detail:   msg,
		}}
		checks := []Check{{
			Name:   "kernel bugs",
			OK:     true,
			Detail: msg,
		}}
		return checks, res
	}

	var checks []Check
	results := make([]api.KernelBugResult, 0, len(list.Bugs))
	for _, bug := range list.Bugs {
		res, chk, emitCheck := evaluateOneKernelBug(bug, distroID, versionID, pkgVer, upVer, hasUp, env)
		results = append(results, res)
		if emitCheck {
			checks = append(checks, chk)
		}
	}
	return checks, results
}

func evaluateOneKernelBug(
	bug KernelBug,
	distroID, versionID, pkgVer string,
	upVer kernelTriple,
	hasUp bool,
	env KernelBugEnv,
) (api.KernelBugResult, Check, bool) {
	status := api.KernelBugFixed
	fixedPkg := ""
	matchedDistro := false

	if pkgVer != "" {
		if row, ok := matchDistroRow(bug.Distro, distroID, versionID, pkgVer); ok {
			matchedDistro = true
			fixedPkg = row.Fixed
			if CompareDebianVersion(pkgVer, row.Fixed) < 0 {
				status = api.KernelBugAffected
			}
		}
	}
	if !matchedDistro && hasUp {
		if matchesUpstreamRanges(upVer, bug.Upstream) {
			status = api.KernelBugMaybe
		}
	}

	if (status == api.KernelBugAffected || status == api.KernelBugMaybe) && len(bug.MitigatedBy) > 0 {
		if reasons, ok := checkBugMitigations(bug.MitigatedBy, env); ok {
			detail := fmt.Sprintf("%s (%s): mitigated, %s.", bug.Name, bug.ID, strings.Join(reasons, ", "))
			res := api.KernelBugResult{
				ID:           bug.ID,
				Name:         bug.Name,
				Status:       api.KernelBugMitigated,
				Severity:     "ok",
				Detail:       detail,
				URL:          bug.URL,
				FixedVersion: fixedPkg,
			}
			return res, Check{Name: "kernel " + bug.ID, OK: true, Detail: detail}, true
		}
	}

	switch status {
	case api.KernelBugAffected:
		detail := fmt.Sprintf("Kernel %s has %s (%s), fixed in package %s. Update the kernel, then reboot.", pkgVer, bug.Name, bug.ID, fixedPkg)
		fix := kernelBugFixHint(bug, fixedPkg)
		res := api.KernelBugResult{
			ID:           bug.ID,
			Name:         bug.Name,
			Status:       api.KernelBugAffected,
			Severity:     "warn",
			Detail:       detail,
			Fix:          fix,
			URL:          bug.URL,
			FixedVersion: fixedPkg,
		}
		return res, Check{Name: "kernel " + bug.ID, OK: false, Detail: detail, Fix: fix}, true
	case api.KernelBugMaybe:
		detail := fmt.Sprintf("Kernel %s is in the range of %s (%s). Your distribution may have fixed it: check its tracker.", upVer.String(), bug.Name, bug.ID)
		fix := kernelBugFixHint(bug, "")
		res := api.KernelBugResult{
			ID:       bug.ID,
			Name:     bug.Name,
			Status:   api.KernelBugMaybe,
			Severity: "warn",
			Detail:   detail,
			Fix:      fix,
			URL:      bug.URL,
		}
		return res, Check{Name: "kernel " + bug.ID, OK: false, Detail: detail, Fix: fix}, true
	default:
		return api.KernelBugResult{
			ID:           bug.ID,
			Name:         bug.Name,
			Status:       api.KernelBugFixed,
			URL:          bug.URL,
			FixedVersion: fixedPkg,
		}, Check{}, false
	}
}

func kernelBugFixHint(bug KernelBug, fixedPkg string) string {
	var parts []string
	if fixedPkg != "" {
		parts = append(parts, fmt.Sprintf("update the kernel package to %s or later and reboot", fixedPkg))
	} else {
		parts = append(parts, fmt.Sprintf("check your distribution's security tracker for %s and update the kernel if needed", bug.ID))
	}
	if bug.RootStep != "" {
		parts = append(parts, fmt.Sprintf("until then, apply the %s mitigation step", bug.RootStep))
	}
	return strings.Join(parts, "; ")
}

func matchDistroRow(rows []KernelBugDistro, distroID, versionID, pkgVer string) (KernelBugDistro, bool) {
	if distroID == "" || versionID == "" {
		return KernelBugDistro{}, false
	}
	pkgSeries := kernelSeriesOf(pkgVer)
	for _, r := range rows {
		if !strings.EqualFold(r.ID, distroID) || r.VersionID != versionID {
			continue
		}
		if r.Series != "" && pkgSeries != r.Series {
			continue
		}
		return r, true
	}
	return KernelBugDistro{}, false
}

func kernelSeriesOf(ver string) string {
	maj, minVal, ok := api.ParseKernelMajorMinor(ver)
	if !ok {
		return ""
	}
	return fmt.Sprintf("%d.%d", maj, minVal)
}

func checkBugMitigations(conds []string, env KernelBugEnv) ([]string, bool) {
	if len(conds) == 0 {
		return nil, false
	}
	reasons := make([]string, 0, len(conds))
	for _, cond := range conds {
		k, v, ok := strings.Cut(strings.TrimSpace(cond), "=")
		if !ok || k == "" || v == "" {
			return nil, false
		}
		if modName, isMod := strings.CutPrefix(k, "module:"); isMod {
			if v != "absent" || env.ModuleLoaded == nil || env.ModuleLoaded(modName) {
				return nil, false
			}
			reasons = append(reasons, modName+" is not loaded")
			continue
		}
		if env.Facts == nil || env.Facts[k] != v {
			return nil, false
		}
		reasons = append(reasons, k+"="+v)
	}
	return reasons, true
}

// ParseOSReleaseIDVersion extracts ID and VERSION_ID from /etc/os-release bytes.
func ParseOSReleaseIDVersion(b []byte) (id, versionID string) {
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, "ID="); ok {
			id = strings.ToLower(strings.Trim(v, `"'`))
		}
		if v, ok := strings.CutPrefix(line, "VERSION_ID="); ok {
			versionID = strings.Trim(v, `"'`)
		}
	}
	return id, versionID
}

func extractDistroPkgVersion(distroID, unameV, versionSig string) string {
	switch distroID {
	case "debian":
		return ExtractDebianPkgVersion(unameV)
	case "ubuntu":
		return ExtractUbuntuPkgVersion(versionSig)
	default:
		return ""
	}
}

// ExtractDebianPkgVersion parses the Debian kernel package version written
// after "Debian " in `uname -v`, e.g. "#1 SMP PREEMPT_DYNAMIC Debian 6.12.118-1 (2026-11-20)".
func ExtractDebianPkgVersion(unameV string) string {
	_, after, ok := strings.Cut(unameV, "Debian ")
	if !ok {
		return ""
	}
	fields := strings.Fields(after)
	if len(fields) == 0 {
		return ""
	}
	tok := fields[0]
	if _, _, valid := api.ParseKernelMajorMinor(tok); !valid {
		return ""
	}
	return tok
}

// ExtractUbuntuPkgVersion parses the Ubuntu kernel package version from the
// second field of /proc/version_signature and strips its trailing flavour
// (for example "Ubuntu 6.8.0-45.45-generic 6.8.12" -> "6.8.0-45.45").
func ExtractUbuntuPkgVersion(versionSig string) string {
	fields := strings.Fields(versionSig)
	if len(fields) < 2 {
		return ""
	}
	tok := fields[1]
	// Ubuntu encodes <upstream>-<abi>.<upload>[~<backport>]-<flavour>
	// (such as 6.8.0-45.45-generic-64k or 7.0.0-28.28~24.04.1-generic). Keep
	// <upstream>-<abi>.<upload>[~<backport>] by cutting at the second hyphen so
	// flavours that start with digits (such as -generic-64k or -aws-64k) are
	// stripped cleanly.
	if first := strings.IndexByte(tok, '-'); first >= 0 {
		if second := strings.IndexByte(tok[first+1:], '-'); second >= 0 {
			tok = tok[:first+1+second]
		}
	}
	if _, _, valid := api.ParseKernelMajorMinor(tok); !valid {
		return ""
	}
	return tok
}

type kernelTriple struct {
	major int
	minor int
	patch int
}

func (k kernelTriple) String() string {
	return fmt.Sprintf("%d.%d.%d", k.major, k.minor, k.patch)
}

func (k kernelTriple) compare(other kernelTriple) int {
	if k.major != other.major {
		if k.major < other.major {
			return -1
		}
		return 1
	}
	if k.minor != other.minor {
		if k.minor < other.minor {
			return -1
		}
		return 1
	}
	if k.patch != other.patch {
		if k.patch < other.patch {
			return -1
		}
		return 1
	}
	return 0
}

func parseKernelTriple(release string) (kernelTriple, bool) {
	release = strings.TrimSpace(release)
	parts := strings.SplitN(release, ".", 4)
	if len(parts) < 3 {
		return kernelTriple{}, false
	}
	maj, ok1 := parseLeadingInt(parts[0])
	minVal, ok2 := parseLeadingInt(parts[1])
	patch, ok3 := parseLeadingInt(parts[2])
	if !ok1 || !ok2 || !ok3 {
		return kernelTriple{}, false
	}
	return kernelTriple{major: maj, minor: minVal, patch: patch}, true
}

func parseLeadingInt(s string) (int, bool) {
	end := 0
	for end < len(s) && isASCIIDigit(s[end]) {
		end++
	}
	if end == 0 {
		return 0, false
	}
	n, err := strconv.Atoi(s[:end])
	if err != nil {
		return 0, false
	}
	return n, true
}

func matchesUpstreamRanges(v kernelTriple, ranges []KernelBugRange) bool {
	for _, r := range ranges {
		from, ok := parseKernelTriple(r.From)
		if !ok {
			continue
		}
		if r.Fixed == "" {
			if v.major == from.major && v.minor == from.minor && v.patch >= from.patch {
				return true
			}
			continue
		}
		fixed, ok := parseKernelTriple(r.Fixed)
		if !ok {
			continue
		}
		if v.compare(from) >= 0 && v.compare(fixed) < 0 {
			return true
		}
	}
	return false
}
