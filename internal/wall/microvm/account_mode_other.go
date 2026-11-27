//go:build !linux

package microvm

import (
	"context"
	"fmt"
	"net"

	"github.com/iamlakshya/simstim/internal/vmneed"
	"github.com/iamlakshya/simstim/internal/wall"
)

func OriginalAccountModeV1(context.Context, wall.Sandbox, net.Conn, *NativeBindingV1, vmneed.NeedV1) (bool, error) {
	return false, fmt.Errorf("a native account observation needs the original Linux VM issuer")
}
