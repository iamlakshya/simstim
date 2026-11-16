package proc

import (
	"errors"
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

func TestAwaitStatusErrorRetainsActualNativeNotAChildFailure(t *testing.T) {
	// This process cannot be its own child. No stale PID or reaped child is
	// observed, and WNOWAIT cannot release any process identity here.
	if _, known, err := AwaitStatusError(os.Getpid()); known || !errors.Is(err, unix.ECHILD) {
		t.Fatalf("native ECHILD was hidden or turned into exit: %v %v", known, err)
	}
	if _, known := AwaitStatus(os.Getpid()); known {
		t.Fatal("the existing wrapper changed its non-child result")
	}
}
