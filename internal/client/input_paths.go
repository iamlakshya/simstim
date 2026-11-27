package client

import (
	"path/filepath"
	"runtime"
	"strings"
)

func localInputPath(value, originalCwd string) string {
	if filepath.IsAbs(value) {
		return value
	}
	return originalCwd + string(filepath.Separator) + value
}

func expandLocalInputHome(value, home string) string {
	if value == "~" {
		return home
	}
	if strings.HasPrefix(value, "~/") || localWindows() && strings.HasPrefix(value, "~\\") {
		return home + string(filepath.Separator) + value[2:]
	}
	return value
}

func localWindows() bool { return runtime.GOOS == "windows" }
