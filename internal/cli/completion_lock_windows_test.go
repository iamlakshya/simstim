//go:build windows

package cli

import "testing"

// The blocking file-lock witness is Unix-only. The rest of the completion
// contract remains checked without a Unix flock on Windows.
func holdCompletionHostsFileLock(_ *testing.T) {}
