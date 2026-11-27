//go:build !linux

package host

import (
	"context"
	"net"
	"os"
)

type originalSlotStartupV2 struct{}

func (receipt *OriginalVMNeed) HasOriginalCheckpointReservationV2(context.Context) (bool, error) {
	if receipt != nil && receipt.boot != nil && receipt.boot.slotReservation != nil {
		return false, vmNeedError("reserved native startup needs its original Linux creator")
	}
	return false, nil
}
func (receipt *OriginalVMNeed) WithOriginalCheckpointReservationV2(context.Context, func([]*os.File) error) (bool, error) {
	return false, vmNeedError("reserved native startup needs its original Linux creator")
}
func (receipt *OriginalVMNeed) finishOriginalCheckpointReservationV2(context.Context, net.Conn) error {
	if receipt != nil && receipt.boot != nil && receipt.boot.slotReservation != nil {
		return vmNeedError("reserved native startup needs its original Linux creator")
	}
	return nil
}
