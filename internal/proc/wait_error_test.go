//go:build linux || darwin

package proc

import (
	"errors"
	"testing"

	"golang.org/x/sys/unix"
)

func TestAwaitStatusErrorRejectsInvalidPIDWithoutExitEvidence(t *testing.T) {
	for _, pid := range []int{0, -1} {
		if _, known, err := AwaitStatusError(pid); known || !errors.Is(err, unix.EINVAL) {
			t.Fatalf("invalid pid acquired exit evidence or lost its error: %d %v %v", pid, known, err)
		}
		if _, known := AwaitStatus(pid); known {
			t.Fatalf("the existing wrapper acquired exit evidence: %d", pid)
		}
	}
}
