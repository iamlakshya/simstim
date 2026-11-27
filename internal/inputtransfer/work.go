package inputtransfer

import (
	"encoding/json"
	"fmt"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// WireWork bounds accepted domain framing from the authenticated declaration.
// It preserves the existing JSON contract, including control whitespace, and
// permits one-byte positive data chunks. The resulting absolute allocation is
// deliberately looser than canonical Send output. It is not measured network
// traffic, a throughput claim or a relaxation of any performance requirement.
// Link/transport control noise cannot refresh idle or enlarge this allocation.
func WireWork(summary api.InputSummary) (int64, error) {
	const maximum = int64(1<<63 - 1)
	if err := summary.Validate(maximum, maximum); err != nil {
		return 0, err
	}
	data, err := json.Marshal(summary)
	if err != nil || len(data)+1 > proto.MaxFrame {
		return 0, protocol("authenticated input metadata exceeds the ordinary request bound")
	}
	var total int64
	add := func(value int64) error {
		if value < 0 || value > maximum-total {
			return fmt.Errorf("input wire allocation overflows. Try: select a smaller declared input set")
		}
		total += value
		return nil
	}
	multiply := func(value, count int64) error {
		if value < 0 || count < 0 || count != 0 && value > maximum/count {
			return fmt.Errorf("input wire allocation overflows. Try: select a smaller declared input set")
		}
		return add(value * count)
	}
	const controlWire = int64(FrameMax + 4) // encoded length includes type
	// Summary is semantically compared with the protected request, while its
	// accepted raw JSON may contain whitespace up to the existing4MiB bound.
	// Every positive chunk can carry one byte plus its five framing bytes.
	if err = multiply(proto.MaxFrame-1, 6); err != nil {
		return 0, err
	}
	// Summary begin/end; declarations_end; data_end; ready/staged/complete;
	// one bounded failure and two physical stream open/close envelopes.
	if err = multiply(controlWire, 12); err != nil {
		return 0, err
	}
	// A path is bounded to4095 bytes, but the existing JSON contract permits
	// padding. Use its ACTUAL64KiB accepted control bound, not a new grammar.
	if err = multiply(controlWire, int64(summary.FileCount)*3); err != nil {
		return 0, err
	}
	if err = multiply(summary.CopyBytes, 6); err != nil {
		return 0, err
	}
	for _, repository := range summary.Repositories {
		if err = multiply(controlWire, 2); err != nil {
			return 0, err
		}
		if err = multiply(repository.Size, 6); err != nil {
			return 0, err
		}
	}
	return total, nil
}
