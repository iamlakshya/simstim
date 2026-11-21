//go:build !linux

package supply

import (
	"context"
	"fmt"
	"os"
)

type execModeStateV1 struct{}

func (*ExecModeLeaseV1) CheckProgramV1(context.Context, *os.File) error {
	return fmt.Errorf("native VMM mode phases require Linux")
}
func (*ExecModeLeaseV1) RetainForJailV1(context.Context) (*os.File, error) {
	return nil, fmt.Errorf("native VMM mode phases require Linux")
}
func (*ExecModeLeaseV1) ArmV1(context.Context) error {
	return fmt.Errorf("native VMM mode phases require Linux")
}
func (*ExecModeLeaseV1) RestoreFinalV1(context.Context) error {
	return fmt.Errorf("native VMM mode phases require Linux")
}
func (*ExecModeLeaseV1) Close() error { return nil }
func (*ExecModeLeaseV1) CheckProjectionRecordV1(context.Context, ExecModeRecordV1) error {
	return fmt.Errorf("native VMM mode phases require Linux")
}
func (*ExecModeLeaseV1) CheckArmedRecordV1(context.Context, ExecModeRecordV1) error {
	return fmt.Errorf("native VMM mode phases require Linux")
}
