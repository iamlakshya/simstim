package host

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
)

// This collector runs only local bounded metadata reads. It changes no module,
// kernel setting, file, floor or admission. A nil probe means its other facts
// are unknown, so a zero value cannot manufacture a mitigation.
func kernelBugReportV1(ctx context.Context, source capSource, release string, probe *api.CapProbe) ([]api.KernelBugResultV1, []Check) {
	if source.os != "linux" {
		return nil, nil
	}
	catalog, err := loadKernelBugCatalogV1()
	if err != nil {
		return nil, []Check{{Name: "kernel bug list", OK: true, Detail: "The list of known kernel bugs could not be read.", Fix: "Update the host helper and check its embedded advisory list."}}
	}
	facts := kernelBugFactsV1{
		Release:      strings.TrimSpace(release),
		Distro:       probeOSReleaseFieldsV1(source.read("/etc/os-release")),
		Probe:        probe,
		ModuleAbsent: collectKernelModuleFactsV1(catalog, source),
	}
	if facts.Release == "" {
		facts.Release = strings.TrimSpace(string(source.read("/proc/sys/kernel/osrelease")))
	}
	if facts.Release == "" {
		facts.Release = strings.TrimSpace(capOutput(ctx, source, "uname", "-r"))
	}
	facts.Version = strings.TrimSpace(string(source.read("/proc/sys/kernel/version")))
	if facts.Version == "" {
		facts.Version = strings.TrimSpace(capOutput(ctx, source, "uname", "-v"))
	}
	facts.Signature = strings.TrimSpace(string(source.read("/proc/version_signature")))
	results := make([]api.KernelBugResultV1, 0, len(catalog.Bugs))
	checks := []Check{}
	unknownShown := false
	for _, bug := range catalog.Bugs {
		result := matchKernelBugV1(bug, facts)
		results = append(results, result)
		if result.Result == "fixed" {
			continue
		}
		if result.Result == "unknown" {
			if unknownShown {
				continue
			}
			unknownShown = true
		}
		checks = append(checks, Check{Name: "kernel: " + result.ID, OK: result.Severity != "warn", Detail: result.Detail, Fix: result.Hint})
	}
	return results, checks
}

func collectKernelModuleFactsV1(catalog kernelBugCatalogV1, source capSource) map[string]bool {
	absent := map[string]bool{}
	for _, bug := range catalog.Bugs {
		for _, condition := range bug.MitigatedBy {
			name, _, _ := strings.Cut(condition, "=")
			module, ok := strings.CutPrefix(name, "module:")
			if !ok {
				continue
			}
			if _, seen := absent[module]; !seen {
				absent[module] = kernelModuleAbsentV1(source, module)
			}
		}
	}
	return absent
}

// kernelModuleAbsentV1 reports true only when /sys/module is an observable
// directory and Lstat confirms the named module entry does not exist. A
// missing, non-directory or unreadable /sys/module parent, or a broken symlink
// at the module path, never establishes absence. Whether a built-in (=y)
// driver can still be active without a /sys/module/<name> entry remains open.
func kernelModuleAbsentV1(source capSource, module string) bool {
	if !kernelModuleNameV1.MatchString(module) {
		return false
	}
	parent := source.path("/sys/module")
	info, err := os.Lstat(parent)
	if err != nil || !info.IsDir() {
		return false
	}
	dir, err := os.Open(parent)
	if err != nil {
		return false
	}
	_, readErr := dir.Readdirnames(1)
	_ = dir.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return false
	}
	_, err = os.Lstat(filepath.Join(parent, module))
	return errors.Is(err, os.ErrNotExist)
}
