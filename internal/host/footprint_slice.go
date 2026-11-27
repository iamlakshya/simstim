package host

import (
	"context"
	"strconv"
)

// writeFootprintedSliceV1 prepares only this fixed MemoryMax drop-in. The
// caller must apply it through the authenticated manager and verify the real
// cgroup value; successful file publication alone never proves a live cap.
func writeFootprintedSliceV1(ctx context.Context, limit int64) error {
	if limit <= 0 {
		return placementError("the slice footprint has no positive memory limit")
	}
	text := "[Slice]\nMemoryMax=" + strconv.FormatInt(limit, 10) + "\n"
	return writeFootprintedFileV1(ctx, "slice_control", "50-MemoryMax.conf", []byte(text))
}
