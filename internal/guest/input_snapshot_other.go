//go:build !linux

package guest

import (
	"context"
	"os"

	"github.com/iamlakshya/simstim/internal/api"
)

func sealInputBundle(context.Context, *os.File, api.InputRepository, int64) (*os.File, error) {
	return nil, inputError("immutable seed descriptors require a Linux guest")
}
