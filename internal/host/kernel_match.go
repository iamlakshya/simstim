package host

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
)

type kernelBugFactsV1 struct {
	Release      string
	Version      string
	Signature    string
	Distro       map[string]string
	Probe        *api.CapProbe
	ModuleAbsent map[string]bool
}

func matchKernelBugV1(bug kernelBugV1, facts kernelBugFactsV1) api.KernelBugResultV1 {
	result := api.KernelBugResultV1{ID: bug.ID, Name: bug.Name, URL: bug.URL, Kernel: api.CleanText(facts.Release, 256), Result: "unknown", Severity: "ok"}
	release, releaseErr := parseKernelVersionV1(facts.Release)
	if distroResult, matched := matchKernelDistroBugV1(result, bug, facts, release, releaseErr == nil); matched {
		return distroResult
	}
	return matchKernelUpstreamBugV1(result, bug, facts, release, releaseErr)
}

func matchKernelDistroBugV1(result api.KernelBugResultV1, bug kernelBugV1, facts kernelBugFactsV1, release kernelVersionV1, known bool) (api.KernelBugResultV1, bool) {
	for _, row := range bug.Distro {
		if row.ID != facts.Distro["ID"] || row.VersionID != facts.Distro["VERSION_ID"] {
			continue
		}
		if !kernelPackageScopeV1(row, facts, release, known) {
			continue
		}
		running := runningKernelPackageV1(row, facts)
		if running == "" {
			result.Detail = "The running kernel package version could not be verified, so the list of known bugs was not checked."
			return result, true
		}
		result.Package, result.Fixed = running, row.Fixed
		if row.Fixed == "" {
			result.Result = "affected"
			return finishKernelBugResultV1(result, bug, facts), true
		}
		order, err := compareDebianVersionsV1(running, row.Fixed)
		if err != nil {
			result.Detail = "The running kernel package version could not be read, so the list of known bugs was not checked."
			return result, true
		}
		result.Result = "fixed"
		if order < 0 {
			result.Result = "affected"
		}
		return finishKernelBugResultV1(result, bug, facts), true
	}
	return result, false
}

func matchKernelUpstreamBugV1(result api.KernelBugResultV1, bug kernelBugV1, facts kernelBugFactsV1, release kernelVersionV1, releaseErr error) api.KernelBugResultV1 {
	if releaseErr != nil || len(bug.Upstream) == 0 {
		result.Detail = "The kernel's version could not be read, so the list of known bugs was not checked."
		return result
	}
	matched := false
	for _, row := range bug.Upstream {
		first, err := parseCanonicalKernelVersionV1(row.From)
		if err != nil {
			result.Detail = "The list of known kernel bugs has an invalid version range, so it was not checked."
			return result
		}
		var fixed kernelVersionV1
		if row.Fixed != "" {
			fixed, err = parseCanonicalKernelVersionV1(row.Fixed)
			if err != nil || first.compare(fixed) >= 0 {
				result.Detail = "The list of known kernel bugs has an invalid version range, so it was not checked."
				return result
			}
		}
		if release.compare(first) < 0 {
			continue
		}
		if row.Fixed != "" && release.compare(fixed) >= 0 {
			continue
		}
		matched = true
	}
	if matched {
		result.Result = "maybe"
		return finishKernelBugResultV1(result, bug, facts)
	}
	result.Result = "fixed"
	result.Detail = "The kernel is outside the embedded known-affected upstream ranges. This does not verify vendor backport provenance."
	return result
}

func kernelPackageScopeV1(row kernelBugDistroV1, facts kernelBugFactsV1, release kernelVersionV1, known bool) bool {
	if !known || row.ID == "" || row.VersionID == "" {
		return false
	}
	if strings.TrimSpace(row.ID) != row.ID || strings.TrimSpace(row.VersionID) != row.VersionID {
		return false
	}
	if row.Flavour != "" && !validKernelFlavourV1(row.Flavour) {
		return false
	}
	switch row.ID {
	case "ubuntu":
		if row.Flavour == "" {
			return false
		}
		ubuntuRelease, _, flavour, ok := parseUbuntuReleaseV1(facts.Release)
		if !ok || flavour != row.Flavour {
			return false
		}
		release = ubuntuRelease
	case "debian":
		if row.Flavour != "" && !strings.HasSuffix(strings.TrimSpace(facts.Release), "-"+row.Flavour) {
			return false
		}
	default:
		return false
	}
	if row.Fixed != "" {
		return validCatalogDistroFixedScopeV1(row, release)
	}
	return true
}

func validCatalogDistroFixedScopeV1(row kernelBugDistroV1, release kernelVersionV1) bool {
	version, err := parseDebianVersionV1(row.Fixed)
	if err != nil {
		return false
	}
	branch, err := parseCanonicalKernelVersionV1(version.upstream)
	if err != nil || branch.prerelease != "" {
		return false
	}
	if row.ID == "ubuntu" {
		if _, ok := parseUbuntuPackageABIV1(version.revision); !ok {
			return false
		}
		return branch.numbers == release.numbers
	}
	return branch.numbers[0] == release.numbers[0] && branch.numbers[1] == release.numbers[1]
}

func runningKernelPackageV1(row kernelBugDistroV1, facts kernelBugFactsV1) string {
	switch row.ID {
	case "debian":
		return runningDebianPackageV1(facts)
	case "ubuntu":
		return runningUbuntuPackageV1(row, facts)
	default:
		return ""
	}
}

func runningDebianPackageV1(facts kernelBugFactsV1) string {
	release, err := parseKernelVersionV1(facts.Release)
	if err != nil {
		return ""
	}
	var version string
	fields := strings.Fields(facts.Version)
	for index, field := range fields {
		if field == "Debian" && index+1 < len(fields) {
			version = fields[index+1]
			break
		}
	}
	packageVersion, err := parseDebianVersionV1(version)
	if err != nil || strings.Contains(packageVersion.upstream, "-") {
		return ""
	}
	packageBranch, err := parseKernelVersionV1(packageVersion.upstream)
	if err != nil || packageBranch.numbers[0] != release.numbers[0] || packageBranch.numbers[1] != release.numbers[1] {
		return ""
	}
	return version
}

func runningUbuntuPackageV1(row kernelBugDistroV1, facts kernelBugFactsV1) string {
	if !validKernelFlavourV1(row.Flavour) {
		return ""
	}
	ubuntuRelease, ubuntuABI, flavour, ok := parseUbuntuReleaseV1(facts.Release)
	if !ok || flavour != row.Flavour {
		return ""
	}
	fields := strings.Fields(facts.Signature)
	if len(fields) < 2 || len(fields) > 3 || fields[0] != "Ubuntu" {
		return ""
	}
	if len(fields) == 3 {
		mainline, err := parseKernelVersionV1(fields[2])
		if err != nil || mainline.numbers[0] != ubuntuRelease.numbers[0] || mainline.numbers[1] != ubuntuRelease.numbers[1] {
			return ""
		}
	}
	version, ok := strings.CutSuffix(fields[1], "-"+row.Flavour)
	if !ok {
		return ""
	}
	packageVersion, err := parseDebianVersionV1(version)
	if err != nil {
		return ""
	}
	packageBranch, err := parseCanonicalKernelVersionV1(packageVersion.upstream)
	if err != nil || packageBranch.prerelease != "" || packageBranch.numbers != ubuntuRelease.numbers {
		return ""
	}
	sigABI, ok := parseUbuntuPackageABIV1(packageVersion.revision)
	if !ok || sigABI != ubuntuABI {
		return ""
	}
	return version
}

func finishKernelBugResultV1(result api.KernelBugResultV1, bug kernelBugV1, facts kernelBugFactsV1) api.KernelBugResultV1 {
	if result.Result == "fixed" {
		return result
	}
	result.Severity = "warn"
	result.Hint = "Check the kernel advisory and install the distribution's fixed kernel, then reboot with the owner's agreement."
	if len(bug.MitigatedBy) > 0 {
		if allKernelMitigationsHoldV1(bug.MitigatedBy, facts) {
			result.Result, result.Severity = "mitigated", "ok"
			result.Detail = bug.Name + " (" + bug.ID + "): mitigated, " + kernelMitigationDescriptionV1(bug.MitigatedBy) + "."
			result.Hint = "Keep the documented mitigation in place and install the fixed kernel."
			return result
		}
		for _, condition := range bug.MitigatedBy {
			if strings.HasPrefix(condition, "module:vhost_vsock=") {
				result.Hint = "The vsock-modules root step blocks the documented path. Install the fixed kernel, then ask the owner before rebooting."
			}
		}
	}
	switch result.Result {
	case "affected":
		result.Detail = fmt.Sprintf("Kernel %s has %s (%s)", result.Kernel, bug.Name, bug.ID)
		if result.Fixed != "" {
			result.Detail += ", fixed in package " + result.Fixed
		}
		result.Detail += ". Update the kernel, then reboot."
	case "maybe":
		result.Detail = fmt.Sprintf("Kernel %s is in the range of %s (%s). Your distribution may have fixed it: check its tracker.", result.Kernel, bug.Name, bug.ID)
	}
	return result
}

func allKernelMitigationsHoldV1(conditions []string, facts kernelBugFactsV1) bool {
	for _, condition := range conditions {
		if !kernelMitigationHoldsV1(condition, facts) {
			return false
		}
	}
	return true
}

func knownKernelMitigationFactV1(path string) bool {
	_, ok := kernelProbeFactV1(&api.CapProbe{}, path, true)
	return ok
}

func kernelProbeFactV1(probe *api.CapProbe, path string, shapeOnly bool) (string, bool) {
	if probe == nil {
		return "", false
	}
	value := reflect.ValueOf(*probe)
	for _, part := range strings.Split(path, ".") {
		var ok bool
		value, ok = derefProbeValueV1(value, shapeOnly)
		if !ok || value.Kind() != reflect.Struct {
			return "", false
		}
		value, ok = lookupStructJSONFieldV1(value, part)
		if !ok {
			return "", false
		}
	}
	var ok bool
	value, ok = derefProbeValueV1(value, shapeOnly)
	if !ok {
		return "", false
	}
	switch value.Kind() {
	case reflect.Bool:
		return strconv.FormatBool(value.Bool()), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(value.Int(), 10), true
	case reflect.String:
		return value.String(), true
	default:
		return "", false
	}
}

func derefProbeValueV1(value reflect.Value, shapeOnly bool) (reflect.Value, bool) {
	for value.Kind() == reflect.Pointer {
		if !value.IsNil() {
			value = value.Elem()
			continue
		}
		if !shapeOnly {
			return reflect.Value{}, false
		}
		value = reflect.New(value.Type().Elem()).Elem()
	}
	return value, true
}

func lookupStructJSONFieldV1(value reflect.Value, name string) (reflect.Value, bool) {
	for index := range value.NumField() {
		tag := strings.Split(value.Type().Field(index).Tag.Get("json"), ",")[0]
		if tag == name {
			return value.Field(index), true
		}
	}
	return reflect.Value{}, false
}

func kernelMitigationHoldsV1(condition string, facts kernelBugFactsV1) bool {
	path, want, ok := strings.Cut(condition, "=")
	if !ok {
		return false
	}
	if module, ok := strings.CutPrefix(path, "module:"); ok {
		return want == "absent" && facts.ModuleAbsent[module]
	}
	actual, ok := kernelProbeFactV1(facts.Probe, path, false)
	return ok && actual == want
}

func kernelMitigationDescriptionV1(conditions []string) string {
	descriptions := make([]string, 0, len(conditions))
	for _, condition := range conditions {
		path, value, _ := strings.Cut(condition, "=")
		if module, ok := strings.CutPrefix(path, "module:"); ok {
			descriptions = append(descriptions, module+" is not loaded")
		} else {
			descriptions = append(descriptions, path+" is "+value)
		}
	}
	return strings.Join(descriptions, ", ")
}
