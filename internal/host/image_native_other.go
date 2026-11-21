//go:build !linux

package host

import (
	"context"
	"os"
	"os/exec"
)

type imageNativeGuardianV1 struct{}

func newImageNativeGuardianV1(context.Context, *exec.Cmd, *os.File, string) (*imageNativeGuardianV1, error) {
	return nil, UserError("image_native", "native image conversion requires Linux", "Use the original Linux host helper.")
}
func imageNativeUnsupportedV1() error {
	return UserError("image_native", "native image ownership is unavailable on this platform", "Use the original Linux host helper.")
}
func (*imageNativeGuardianV1) childStartedV1(error) error { return imageNativeUnsupportedV1() }
func (*imageNativeGuardianV1) bindScopeV1(context.Context, imageScopeInfo) error {
	return imageNativeUnsupportedV1()
}
func (*imageNativeGuardianV1) releasePlanV1() error { return imageNativeUnsupportedV1() }
func (*imageNativeGuardianV1) joinV1() error        { return imageNativeUnsupportedV1() }
func (*imageNativeGuardianV1) closeV1() error       { return nil }
func markImageGuardianPrivateV1([]*os.File) error {
	return UserError("image_native", "native image conversion requires Linux", "Use the original Linux host helper.")
}
