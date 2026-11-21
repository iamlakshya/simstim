package image

import "fmt"

// ConvertedSizeForDataBlocksV1 reports the canonical converted-file size for
// advisory disk accounting. A byte count creates no image or boot authority.
func ConvertedSizeForDataBlocksV1(blocks int64) (int64, error) {
	if blocks <= 0 || blocks > MaxUncompressed/4096 {
		return 0, fmt.Errorf("image data block count is invalid. Try: refresh the original local image pin")
	}
	return convertedBound(blocks * 4096), nil
}
