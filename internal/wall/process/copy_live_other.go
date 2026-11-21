//go:build !linux

package process

import (
	"context"
	"fmt"
)

func (*CopyOwnerV1) CheckLiveV1(context.Context) error {
	return fmt.Errorf("the original native process runtime needs Linux")
}
