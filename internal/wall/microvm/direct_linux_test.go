//go:build linux

package microvm

import (
	"context"
	"testing"
)

func TestSessionDirectRequiresActualMatchingGuestFailure(t *testing.T) {
	spec := vmFixture(t)
	if direct, err := sessionDirect(spec); err != nil || !direct {
		t.Fatalf("untested session did not default to direct I/O: %v %v", direct, err)
	}
	if err := RecordDirectResult(context.Background(), spec, false); err != nil {
		t.Fatal(err)
	}
	if direct, err := sessionDirect(spec); err != nil || direct {
		t.Fatalf("matching guest failure was ignored: %v %v", direct, err)
	}
	spec.DiskMax++
	if direct, err := sessionDirect(spec); err != nil || !direct {
		t.Fatalf("stale failure selected buffered I/O: %v %v", direct, err)
	}
	spec.DiskMax--
	if err := RecordDirectResult(context.Background(), spec, true); err != nil {
		t.Fatal(err)
	}
	if direct, err := sessionDirect(spec); err != nil || !direct {
		t.Fatalf("actual guest success selected buffered I/O: %v %v", direct, err)
	}
}
