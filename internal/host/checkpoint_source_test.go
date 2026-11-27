package host

import (
	"context"
	"testing"
)

func TestCheckpointSourceRequiresActualNativeRuntimeBeforeStorage(t *testing.T) {
	if source, err := CaptureCheckpointSourceV1(t.Context(), context.Background(), nil, nil); err == nil || source != nil {
		t.Fatal("public data created an original checkpoint source")
	}
	var receipt OriginalVMNeed
	if err := receipt.WithSnapshotDiskV1(t.Context(), nil, nil); err == nil {
		t.Fatal("an empty native receipt supplied a disk")
	}
}
