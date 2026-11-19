package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/secretmem"
)

// SavedRootConverterMode selects the fixed saved-root operation in the
// existing descriptor-only image guardian and converter entrypoints.
const SavedRootConverterMode = "saved-root-v1"
const savedRootConverterMode = SavedRootConverterMode

type savedRootConversionResult struct {
	Version        int    `json:"v"`
	DroppedDevices int    `json:"dropped_devices"`
	Error          string `json:"error,omitempty"`
	Code           string `json:"code,omitempty"`
}

// ConvertSavedImage reuses the one owned converter scope, jail, cancellation
// and join path. Only the helper and fresh output are inherited; raw input
// starts through the existing owned pipe after the real jail starts.
func ConvertSavedImage(ctx context.Context, plan image.SavedRootPlan, source func(context.Context, io.Writer) error, output *os.File) (image.SavedRootReport, error) {
	encoded, err := json.Marshal(plan)
	if err != nil || int64(len(encoded)) > image.MaxManifest {
		return image.SavedRootReport{}, UserError("image_too_large", "the saved-root converter plan exceeds its bound", "Use fewer secret values or changed paths.")
	}
	defer clear(encoded)
	var completed imageGuardianFrame
	var collector image.SaveReviewCollectorV1
	if err := convertScopedWithSource(ctx, convertSavedRoot, encoded, nil, output, &completed, func(ctx context.Context, writer io.Writer) error {
		if err := writeSavedChanges(writer, plan.Changes); err != nil {
			return err
		}
		return source(ctx, writer)
	}, func(raw []byte) error {
		var page image.SaveReviewPageV1
		if !api.ExactJSONFields(raw, page) || json.Unmarshal(raw, &page) != nil || page.ID != plan.SaveID {
			return UserError("image_protocol", "the converter changed its original review token or schema", "Use the current host helper.")
		}
		return collector.Append(page, plan.Ref)
	}); err != nil {
		return image.SavedRootReport{}, err
	}
	var result savedRootConversionResult
	decoder := json.NewDecoder(bytes.NewReader(completed.Output))
	decoder.DisallowUnknownFields()
	if !api.ExactJSONFields(completed.Output, result) || decoder.Decode(&result) != nil {
		return image.SavedRootReport{}, UserError("image_protocol", "the saved-root converter returned invalid metadata", "Upgrade the host helper and try again.")
	}
	var trailing any
	if !errors.Is(decoder.Decode(&trailing), io.EOF) {
		return image.SavedRootReport{}, UserError("image_protocol", "the saved-root converter returned trailing metadata", "Upgrade the host helper and try again.")
	}
	if result.Error != "" {
		return image.SavedRootReport{}, UserError(result.Code, result.Error, "Inspect the sandbox with shell, then repeat image save.")
	}
	if result.Version != 1 || result.DroppedDevices < 0 || result.DroppedDevices > image.MaxEntries {
		return image.SavedRootReport{}, UserError("image_protocol", "the converter returned invalid completion metadata", "Use the current host helper.")
	}
	review, err := collector.Review()
	if err != nil {
		return image.SavedRootReport{}, err
	}
	actual, err := image.Inspect(output)
	if err != nil {
		return image.SavedRootReport{}, err
	}
	if review.ID != plan.SaveID || review.Ref != plan.Ref || review.Size != actual.Size {
		return image.SavedRootReport{}, UserError("image_protocol", "the converter review differs from the actual converted root", "Use the current host helper.")
	}
	return image.SavedRootReport{Flagged: review.Flagged, Excluded: review.Excluded, DroppedDevices: result.DroppedDevices}, nil
}

func runSavedRootConvert(input io.Reader, output io.Writer) error {
	if err := imageWorkerConfinement(); err != nil {
		return err
	}
	encoded, err := receiveImagePlan(input)
	if err != nil {
		return err
	}
	defer clear(encoded)
	if int64(len(encoded)) > image.MaxManifest {
		return UserError("image_too_large", "the saved-root plan exceeds 4 MiB", "Use fewer secret values or changed paths.")
	}
	var plan image.SavedRootPlan
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if !api.ExactJSONFields(encoded, plan) || decoder.Decode(&plan) != nil {
		return UserError("image_bad_layer", "the saved-root plan is malformed", "Use the current helper's owned image-save path.")
	}
	var extra any
	if !errors.Is(decoder.Decode(&extra), io.EOF) {
		return UserError("image_bad_layer", "the saved-root plan has trailing content", "Use the current helper's owned image-save path.")
	}
	if plan.Version != 1 || !image.ValidSaveRef(plan.Ref) || !ValidHostID(plan.SaveID) {
		return UserError("image_protocol", "the converter has no original pending image identity", "Use the current helper's owned image-save path.")
	}
	plan.Changes, err = readSavedChanges(input)
	if err != nil {
		return err
	}
	converted := os.NewFile(4, "saved-root-output")
	defer converted.Close()
	report, err := image.ConvertSavedRoot(context.Background(), plan, input, converted)
	response := savedRootWorkerFrame{Version: 1, DroppedDevices: report.DroppedDevices}
	if err != nil {
		response.Code, response.Error = "image_convert", err.Error()
		var public *proto.Error
		if errors.As(err, &public) {
			response.Code = public.Code
		}
	}
	encoder := json.NewEncoder(output)
	if err != nil {
		return encoder.Encode(response)
	}
	checked, err := image.Inspect(converted)
	if err != nil {
		return err
	}
	review := image.SaveReview{ID: plan.SaveID, Ref: plan.Ref, Size: checked.Size, Flagged: report.Flagged, Excluded: report.Excluded}
	if err := review.Validate(plan.Ref); err != nil {
		return err
	}
	for offset := 0; ; {
		page, err := review.PageV1(offset)
		if err != nil {
			return err
		}
		response.Page = &page
		if err := encoder.Encode(response); err != nil {
			return err
		}
		if page.Done {
			return nil
		}
		offset = page.Next
	}
}

func serveSavedRootGuardian(ctx context.Context, unit string, helper, output *os.File, control io.Reader, result io.Writer) error {
	if err := secretmem.Protect(); err != nil {
		return err
	}
	scope, err := imageAttachScope(ctx, unit)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(result)
	if err := encoder.Encode(imageGuardianFrame{Phase: "ready", InvocationID: scope.InvocationID, ControlGroup: scope.ControlGroup}); err != nil {
		return err
	}
	plan, err := receiveImagePlan(control)
	if err != nil {
		return err
	}
	defer clear(plan)
	spec, err := converterSpec([]string{"/proc/self/fd/3", "image-convert", savedRootConverterMode}, []*os.File{helper, output})
	if err != nil {
		return err
	}
	data, err := runConverterJailOutput(ctx, spec, func(writer io.Writer) error {
		if err := sendConverterPlan(writer, plan); err != nil {
			return err
		}
		_, err := io.Copy(writer, &savedRootChunkReader{input: control})
		return err
	}, func(input io.Reader) ([]byte, error) { return readSavedRootWorker(input, encoder) })
	return reportConverterDone(encoder, control, data, err)
}
