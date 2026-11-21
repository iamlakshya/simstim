package egress

import (
	"errors"
	"runtime"
	"runtime/debug"
)

var errPhysicalH2Toolchain = errors.New("HTTP/2 interception requires the pinned scheduler hooks. Try: build with the repository's pinned Go1.26.11 toolchain and dependencies")

// The pinned x/net public legacy server supplies stream retirement and frame
// selection hooks. Its Go1.27 wrapping server silently ignores those hooks.
// Exact patch admission also binds the original native IO behaviour we test.
func physicalH2CompatibilityError(version string, info *debug.BuildInfo) error {
	if version != "go1.26.11" {
		return errPhysicalH2Toolchain
	}
	if info != nil {
		for _, dependency := range info.Deps {
			if dependency.Path == "golang.org/x/net" && dependency.Version == "v0.62.0" && dependency.Replace == nil {
				return nil
			}
		}
	}
	return errPhysicalH2Toolchain
}

func physicalH2HooksError() error {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return errPhysicalH2Toolchain
	}
	return physicalH2CompatibilityError(runtime.Version(), info)
}
