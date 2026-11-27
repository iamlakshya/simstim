package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"runtime"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/vmneed"
)

func init() { Features = append(Features, api.FeatureResumeImageV1) }

// checkResumeExpectedImageV1 only narrows an existing original operation.
// It does not select or write an image, renew native authority or substitute
// a client row for the protected Need. Absence preserves older wire behavior;
// offline automatic restore continues to retain its original protected Need.
func checkResumeExpectedImageV1(ctx context.Context, operation *SessionOperation, raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	if ctx == nil || operation == nil || operation.native == nil || operation.native.borrowed || !operation.Valid(operation.Primary()) {
		return resumeImageMismatchV1()
	}
	expected, err := vmneed.DecodeImageV1(raw)
	if err != nil {
		return resumeImageMismatchV1()
	}
	protected, err := operation.LoadContext(ctx)
	if err != nil {
		return err
	}
	if protected.Wall == nil || protected.Wall.Level != "microvm" || len(protected.VMNeedV1) == 0 {
		return resumeImageMismatchV1()
	}
	need, err := vmneed.DecodeV1(protected.VMNeedV1, runtime.GOARCH)
	if err != nil {
		return resumeImageMismatchV1()
	}
	if err := need.ValidateWall(runtime.GOARCH, protected.Wall.Level, protected.Wall.Driver, protected.Limits.MemoryMax); err != nil {
		return resumeImageMismatchV1()
	}
	actual, err := vmneed.EncodeImageV1(need.Image)
	if err != nil {
		return resumeImageMismatchV1()
	}
	wanted, err := vmneed.EncodeImageV1(expected)
	if err != nil || !bytes.Equal(actual, wanted) {
		return resumeImageMismatchV1()
	}
	return errors.Join(operation.native.check(ctx), ctx.Err())
}
func resumeImageMismatchV1() error {
	return UserError("image_policy_changed", "the retained resume image differs from the original protected sandbox image", "Keep the sandbox and its original image history; inspect the selected host and current laptop image policy before resuming.")
}
