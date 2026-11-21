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

func (*ExecModeLeaseV1) ReadonlyOriginalV1(context.Context) (bool, error) {
	return false, fmt.Errorf("native original VMM execution requires Linux")
}

func (*ExecModeLeaseV1) OriginalItemV1(context.Context) (Item, error) {
	return Item{}, fmt.Errorf("native original VMM execution requires Linux")
}

func (*ExecModeLeaseV1) ReadonlyNonceV1(context.Context) ([16]byte, error) {
	return [16]byte{}, fmt.Errorf("native original VMM execution requires Linux")
}
