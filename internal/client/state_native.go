package client

import (
	"context"
	"os"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

var stateValidateDescriptor = cachefs.ValidateDescriptor

func stateSafeDescriptor(ctx context.Context, file *os.File) (os.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	info, err := stateValidateDescriptor(file, 0600)
	if stopped := ctx.Err(); stopped != nil {
		return nil, stopped
	}
	if err != nil {
		return nil, badGrantStateError("state descriptor")
	}
	if info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return nil, badGrantStateError("state descriptor")
	}
	return info, nil
}

func stateSafeRoot(ctx context.Context, root *os.Root) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	err := cachefs.ValidateRoot(root)
	if stopped := ctx.Err(); stopped != nil {
		return stopped
	}
	if err != nil {
		return badGrantStateError("state folder")
	}
	return nil
}
