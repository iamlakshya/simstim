package testhome

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
)

// Seven base32 characters carry 35 random bits. The standard temporary
// name uses 32 bits, printed as up to ten decimal digits.
const compactSuffixLen = 7

func compactSuffix() string { return rand.Text()[:compactSuffixLen] }

// compactTemp creates a short random name atomically, with the same private
// mode and collision retry bound as os.MkdirTemp. Existing paths stay untouched.
// The name source is local to this call, so tests can exercise real collisions.
func compactTemp(base, prefix string, suffix func() string) (string, error) {
	for range 10000 {
		dir := filepath.Join(base, prefix+suffix())
		if err := os.Mkdir(dir, 0o700); err != nil {
			if os.IsExist(err) {
				continue
			}
			return "", fmt.Errorf("make a compact temporary folder: %w", err)
		}
		return dir, nil
	}
	return "", &os.PathError{Op: "mkdirtemp", Path: filepath.Join(base, prefix+"*"), Err: os.ErrExist}
}
