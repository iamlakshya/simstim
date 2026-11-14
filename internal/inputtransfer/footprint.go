package inputtransfer

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// StagingFootprint is an admission reservation, not measured disk or RSS.
// SealedBytes covers the largest ONE native bundle memfd, not all bundles.
// The actual memory owner must separately account its existing runtime/heap
// overhead and compare this reservation against its canonical headroom view.
type StagingFootprint struct {
	DiskBytes     int64
	MetadataBytes int64
	SealedBytes   int64
	StagedFiles   int64
}

// PlanStaging computes a conservative bounded reservation from the SAME
// authenticated summary. allocation is the pinned stage filesystem's actual
// allocation unit; page is the native memory page size. It adds no product
// aggregate seed/count cap and refuses overflow before a helper launches.
func PlanStaging(summary api.InputSummary, allocation, page int64) (StagingFootprint, error) {
	const maximum = int64(1<<63 - 1)
	if allocation <= 0 || page <= 0 {
		return StagingFootprint{}, protocol("input staging has no positive allocation geometry")
	}
	if err := summary.Validate(maximum, maximum); err != nil {
		return StagingFootprint{}, err
	}
	metadata, err := json.Marshal(summary)
	if err != nil || len(metadata)+1 > proto.MaxFrame {
		return StagingFootprint{}, protocol("input summary exceeds its ordinary metadata bound")
	}
	round := func(value, unit int64) (int64, error) {
		if value < 0 || value > maximum-(unit-1) {
			return 0, fmt.Errorf("input staging allocation overflows. Try: select a smaller input set")
		}
		return (value + unit - 1) / unit * unit, nil
	}
	add := func(total *int64, value int64) error {
		if value < 0 || value > maximum-*total {
			return fmt.Errorf("input staging allocation overflows. Try: select a smaller input set")
		}
		*total += value
		return nil
	}
	var plan StagingFootprint
	row, err := maxStoredDeclaration(summary)
	if err != nil || summary.FileCount != 0 && row > maximum/int64(summary.FileCount) {
		return plan, protocol("input declaration storage overflows")
	}
	plan.MetadataBytes = row * int64(summary.FileCount)
	rounded, err := round(plan.MetadataBytes, allocation)
	if err != nil {
		return plan, err
	}
	if err = add(&plan.DiskBytes, rounded); err != nil {
		return plan, err
	}
	// Include roles are hidden in the hashed table until it arrives. Reserve
	// all loose bytes plus one allocation unit per possible staged leaf.
	rounded, err = round(summary.CopyBytes, allocation)
	if err != nil {
		return plan, err
	}
	if err = add(&plan.DiskBytes, rounded); err != nil {
		return plan, err
	}
	if int64(summary.FileCount) > maximum/allocation {
		return plan, protocol("input leaf allocation overflows")
	}
	if err = add(&plan.DiskBytes, int64(summary.FileCount)*allocation); err != nil {
		return plan, err
	}
	plan.StagedFiles = int64(summary.FileCount) + int64(len(summary.Repositories)) + 1
	for _, repository := range summary.Repositories {
		rounded, err = round(repository.Size, allocation)
		if err != nil {
			return plan, err
		}
		if err = add(&plan.DiskBytes, rounded); err != nil {
			return plan, err
		}
		sealed, err := round(repository.Size, page)
		if err != nil {
			return plan, err
		}
		plan.SealedBytes = max(plan.SealedBytes, sealed)
	}
	return plan, nil
}

func maxStoredDeclaration(summary api.InputSummary) (int64, error) {
	// '<' is a valid path byte escaped to six JSON bytes. The actual parser
	// permits4095-byte paths. Host->guest/spool re-encoding strips padding.
	path := "/" + strings.Repeat("<", 4094)
	scope := Scope{Nonce: api.InputNonce(strings.Repeat("f", 64)), Digest: strings.Repeat("f", 64)}
	var maximum int
	for _, file := range []api.InputFile{
		{Path: path, Size: summary.CopyBytes, Mode: 0777, SHA256: strings.Repeat("f", 64)},
		{Path: path, Mode: 0777, SHA256: api.EmptyInputHash, Directory: true},
		{Path: path, Size: summary.CopyBytes, Mode: 0777, SHA256: strings.Repeat("f", 64), Include: true},
	} {
		data, err := json.Marshal(event{Event: "input.file", Index: max(0, summary.FileCount-1), Nonce: scope.Nonce, Digest: scope.Digest, File: &file})
		if err != nil {
			return 0, err
		}
		maximum = max(maximum, len(data)+5)
	}
	return int64(maximum), nil
}
