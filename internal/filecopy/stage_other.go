//go:build !linux

package filecopy

import "os"

// The fixed-account put helper runs only in a Linux guest. Keep the package
// available to laptop callers without a named-file guest receive fallback.
func newStage(_ *os.Root) (*os.File, func(string, bool) error, func() error, error) {
	return nil, nil, nil, failure("cp_path", "the file receiver requires a Linux guest")
}
