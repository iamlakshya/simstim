package inputtransfer

import (
	"context"
	"errors"
	"io"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// ReportFailure emits a bounded failure from the actual fixed helper owner.
// Unexpected filesystem/process errors never disclose a laptop or host path.
// The owner closes and joins its stream/process independently of this message.
func ReportFailure(out io.Writer, cause error) error {
	return reportFailure(context.Background(), proto.NewWriter(out), cause)
}

func reportFailure(ctx context.Context, writer *proto.Writer, cause error) error {
	if cause == nil {
		return nil
	}
	failure := &proto.Error{Code: "input_io", Message: "the declared input could not be copied",
		Hint: "Check the input files and session filesystem, then try again."}
	var typed *proto.Error
	if errors.As(cause, &typed) {
		failure = &proto.Error{Code: api.CleanText(typed.Code, 80), Message: api.CleanBlock(typed.Message, api.BlockMax), Hint: api.CleanBlock(typed.Hint, api.BlockMax)}
	}
	return writeJSON(ctx, writer, proto.TError, failure)
}
