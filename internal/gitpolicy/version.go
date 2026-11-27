package gitpolicy

import (
	"regexp"
	"runtime"
	"strconv"
)

var versionPattern = regexp.MustCompile(`^git version (\d+)\.(\d+)(\.\d+)?`)

// HookVersion returns the recognised version and whether the canonical
// argument policy can disable every hook its repository config may define.
// Unknown versions and Git2.54 are refused, preserving the host contract.
func HookVersion(value string) (string, bool) {
	return hookVersionPlatform(value, runtime.GOOS == "windows")
}

func hookVersionPlatform(value string, windows bool) (string, bool) {
	fields := versionPattern.FindStringSubmatch(value)
	if fields == nil {
		return "", false
	}
	if windows {
		minor, err := strconv.Atoi(fields[2])
		// Windows cannot use a POSIX null-device spelling as a directory. The
		// per-event configuration switch is the effective shared guard there.
		return fields[0], err == nil && fields[1] == "2" && minor >= 55
	}
	return fields[0], !(fields[1] == "2" && fields[2] == "54")
}
