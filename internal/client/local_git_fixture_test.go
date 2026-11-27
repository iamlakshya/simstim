package client

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func readGitFixtureCase() (string, error) {
	raw, err := os.ReadFile(".native-case")
	return strings.TrimSpace(string(raw)), err
}

// Package-variable initialisation precedes the real private helper init modes.
// This observes actual startup only; no production test environment exception.
var gitFixtureStartupObserver = func() bool {
	localGitHelperObservation = func(role string) {
		if _, err := readGitFixtureCase(); err != nil {
			return
		}
		status := "ok"
		if validateGitRoleEnvironment(localGitHelper, os.Environ(), runtime.GOOS == "windows") != nil {
			status = "invalid"
		}
		if _, present := os.LookupEnv("LOCAL_GIT_FORBIDDEN_MANAGED_MARKER"); present {
			status = "invalid"
		}
		_ = os.WriteFile(filepath.Join(".", ".startup-"+role), []byte(status), 0600)
	}
	return true
}()
