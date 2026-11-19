package host

import (
	"context"
	"github.com/iamlakshya/simstim/internal/proto"
	"io"
	"os"
)

func validateArchiveDescriptor(ctx context.Context, file *os.File, validate func([]byte) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(proto.MaxFrame)+1))
	if err != nil || len(data) > proto.MaxFrame {
		return placementError("the metadata archive exceeds its bound or could not be read")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validate(data); err != nil {
		return err
	}
	return ctx.Err()
}
