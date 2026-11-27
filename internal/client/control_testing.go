package client

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ConfigureControlFallbackForTesting installs the existing fallback factory
// for repository test helpers. Call it before starting test work and restore
// it after all that work ends. It changes no production default, saved data,
// wire field or trust decision. Every normal ownership and bind check remains.
func ConfigureControlFallbackForTesting(dir string) (func(), error) {
	if !controlMaster {
		if dir != "" {
			return nil, errors.New("this platform does not share SSH connections")
		}
		return func() {}, nil
	}
	if !filepath.IsAbs(dir) || !controlFits(dir) || strings.ContainsAny(dir, "\r\n\x00") || !privateDir(dir) {
		return nil, errors.New("the test control directory is not private or exceeds the full bind budget")
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, fmt.Errorf("inspect the test control root: %w", err)
	}
	old := controlFallbackDir
	controlFallbackDir = func(uint32) string {
		current, err := os.Lstat(dir)
		if err != nil || !os.SameFile(info, current) || !privateDir(dir) {
			return ""
		}
		return dir
	}
	return func() { controlFallbackDir = old }, nil
}
