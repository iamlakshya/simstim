//go:build !linux

package process

import (
	"context"
	"io"
	"net"

	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/wall"
)

type copyOwnerNativeV1 struct{}

func NativeCopyOwnerV1(wall.Sandbox, net.Conn) (*CopyOwnerV1, error) { return nil, nil }
func (*CopyOwnerV1) Receive(context.Context, proto.FilePutHeaderV1, io.Reader) (string, error) {
	return "", setupError("no original process copy factory exists on this platform", nil)
}
