package egress

import (
	"errors"
	"runtime"
	"runtime/debug"
	"strings"
)

const physicalH2QualifiedRuntime = "go1.27.4"
const physicalH2QualifiedModuleSum = "h1:18ELalfM5+XNRwzM2nYqXsZQ9ByS1uhj2O/U7+5jqRY="

var errPhysicalH2Toolchain = errors.New("HTTP/2 interception requires qualified legacy hooks. Build with exact dependencies and source-owned userns_cohort_v3,http2legacy tags. Try: GOTOOLCHAIN=go1.27.4 just helpers")

// The exact public legacy server supplies stream retirement and native frame
// selection. Its wrapping server omits those hooks. Both actual compilation
// and actual recorded metadata must identify the supported legacy selection.
func physicalH2CompatibilityError(version string, info *debug.BuildInfo) error {
	if version != physicalH2QualifiedRuntime || info == nil || info.GoVersion != physicalH2QualifiedRuntime || !physicalH2LegacyTagRecorded(info.Settings) {
		return errPhysicalH2Toolchain
	}
	found := false
	for _, dependency := range info.Deps {
		if dependency == nil {
			return errPhysicalH2Toolchain
		}
		if dependency.Path != "golang.org/x/net" {
			continue
		}
		if found || dependency.Version != "v0.62.0" || dependency.Sum != physicalH2QualifiedModuleSum || dependency.Replace != nil {
			return errPhysicalH2Toolchain
		}
		found = true
	}
	if !found {
		return errPhysicalH2Toolchain
	}
	return nil
}

// The pinned official producer writes BuildContext.BuildTags as one comma
// list. Require an exact tag, never a substring or an inferred source path.
func physicalH2LegacyTagRecorded(settings []debug.BuildSetting) bool {
	seen, legacy := false, false
	for _, setting := range settings {
		if setting.Key != "-tags" {
			continue
		}
		if seen {
			return false
		}
		seen = true
		for _, tag := range strings.Split(setting.Value, ",") {
			if tag == "" || strings.TrimSpace(tag) != tag {
				return false
			}
			legacy = legacy || tag == "http2legacy"
		}
	}
	return seen && legacy
}

func physicalH2HooksError() error {
	if !physicalH2LegacyServerCompiled {
		return errPhysicalH2Toolchain
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return errPhysicalH2Toolchain
	}
	return physicalH2CompatibilityError(runtime.Version(), info)
}
