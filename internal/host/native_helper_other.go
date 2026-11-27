//go:build !linux

package host

import "github.com/iamlakshya/simstim/internal/proto"

func (*StartupLineV1) WriteNativeHelloV1(proto.Hello) error {
	return sessionLeaseError("native code/namespace startup requires a supported Linux host")
}
func (*StartupLineV1) AcceptNativeHelloV1(proto.Frame) error {
	return sessionLeaseError("native code/namespace startup requires a supported Linux host")
}
