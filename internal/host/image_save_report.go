package host

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/image"
)

type savedRootWorkerFrame struct {
	Version        int                     `json:"v"`
	Page           *image.SaveReviewPageV1 `json:"page,omitempty"`
	DroppedDevices int                     `json:"dropped_devices"`
	Error          string                  `json:"error,omitempty"`
	Code           string                  `json:"code,omitempty"`
}

// Pages retain their actual pending ID from the native Store producer. This
// guardian forwards each bounded window and never materialises the full review.
func readSavedRootWorker(input io.Reader, output *json.Encoder) ([]byte, error) {
	reader := bufio.NewReaderSize(input, image.SaveReviewPageBytes)
	var first *image.SaveReviewPageV1
	next, dropped := 0, 0
	complete, refused := false, false
	result := savedRootConversionResult{Version: 1}
	for {
		raw, err := reader.ReadSlice('\n')
		if errors.Is(err, io.EOF) && len(raw) == 0 {
			if !complete && !refused {
				return nil, imageSaveReportFailure()
			}
			return json.Marshal(result)
		}
		if err != nil || len(raw) > image.SaveReviewPageBytes || complete || refused {
			return nil, imageSaveReportFailure()
		}
		var frame savedRootWorkerFrame
		if !api.ExactJSONFields(raw, frame) || json.Unmarshal(raw, &frame) != nil || frame.Version != 1 || frame.DroppedDevices < 0 || frame.DroppedDevices > image.MaxEntries {
			return nil, imageSaveReportFailure()
		}
		if frame.Error != "" {
			if frame.Page != nil || frame.Code == "" || next != 0 {
				return nil, imageSaveReportFailure()
			}
			result.Error, result.Code, refused = frame.Error, frame.Code, true
			continue
		}
		if frame.Page == nil || frame.Code != "" {
			return nil, imageSaveReportFailure()
		}
		page := frame.Page
		id := ""
		if first != nil {
			id = first.ID
		}
		if err := page.Validate(page.Ref, id, next); err != nil {
			return nil, err
		}
		if first == nil {
			copy := *page
			first = &copy
			dropped = frame.DroppedDevices
		} else if first.Ref != page.Ref || first.Size != page.Size || first.FlaggedTotal != page.FlaggedTotal || first.ExcludedTotal != page.ExcludedTotal || dropped != frame.DroppedDevices {
			return nil, imageSaveReportFailure()
		}
		encoded, err := json.Marshal(page)
		if err != nil {
			return nil, err
		}
		if err := output.Encode(imageGuardianFrame{Phase: "review", Output: encoded}); err != nil {
			return nil, err
		}
		next, complete = page.Next, page.Done
		result.DroppedDevices = dropped
	}
}

func imageSaveReportFailure() error {
	return UserError("image_protocol", "the owned converter returned an invalid or incomplete review page stream", "Use the current host helper and repeat image save.")
}

// The saved operation has a bounded frame reader, not the OCI operation's
// bounded whole result. The canonical collector limits total progress/data.
func imageSaveGuardianFrames(ctx context.Context, input io.Reader) (<-chan imageFrameResult, <-chan struct{}) {
	frames := make(chan imageFrameResult, 2)
	done := make(chan struct{})
	go func() {
		defer close(frames)
		defer close(done)
		reader := bufio.NewReaderSize(input, 64<<10)
		send := func(result imageFrameResult) bool {
			select {
			case frames <- result:
				return true
			case <-ctx.Done():
				return false
			}
		}
		for {
			raw, err := reader.ReadSlice('\n')
			if err != nil {
				send(imageFrameResult{err: err})
				return
			}
			var frame imageGuardianFrame
			if !api.ExactJSONFields(raw, frame) || json.Unmarshal(raw, &frame) != nil {
				send(imageFrameResult{err: imageSaveReportFailure()})
				return
			}
			if !send(imageFrameResult{frame: frame}) {
				return
			}
			if frame.Phase == "done" {
				return
			}
		}
	}()
	return frames, done
}

func savedRootOutcomeFailure(raw []byte) error {
	var result savedRootConversionResult
	if !api.ExactJSONFields(raw, result) || json.Unmarshal(raw, &result) != nil || result.Version != 1 || result.Error == "" || result.Code == "" {
		return nil
	}
	return UserError(result.Code, result.Error, "Inspect the sandbox with shell, then repeat image save.")
}
