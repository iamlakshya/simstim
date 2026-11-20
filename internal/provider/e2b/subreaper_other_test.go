//go:build !linux && !windows

package e2b

import "testing"

// ownEscaped cannot keep an orphaned grandchild as our own child here. macOS
// has no subreaper, such as Linux's PR_SET_CHILD_SUBREAPER, so launchd owns
// and reaps the grandchild once its wrapper exits. The test proves that the
// grandchild ended through the alive FIFO's EOF.
func ownEscaped(*testing.T) func(bool) {
	return func(bool) {}
}
