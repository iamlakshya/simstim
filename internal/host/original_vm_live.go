package host

import (
	"context"
	"net"
)

// CheckRunningNativeV1 consumes only this original receipt's SAME prepared
// native engine and exact Start connection. It grants no new startup or image
// selection and cannot be recreated from a public Need or status.
func (receipt *OriginalVMNeed) CheckRunningNativeV1(ctx context.Context, connection net.Conn) error {
	if receipt == nil || OriginalVMNeedFromContext(ctx) != receipt || connection == nil {
		return vmNeedError("the running native VM has no original receipt/connection")
	}
	receipt.prepareMu.Lock()
	defer receipt.prepareMu.Unlock()
	if receipt.need == nil || !receipt.prepareUsed || !receipt.prepareConfirmed || receipt.binding == nil || receipt.connection != connection || receipt.boot == nil || receipt.boot.owner == nil || !receipt.boot.owner.Valid(receipt.boot.loan.Primary) {
		return vmNeedError("the original prepared VM lifetime ended or changed")
	}
	return receipt.binding.Check(ctx, connection, *receipt.need)
}
