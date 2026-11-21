//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/wall/microvm"
)

// SendCheckpointLoanV1 issues only this receipt's genuine original slot
// producer on the SAME accepted outside native request lane. The caller
// supplies no source vector or replacement constructor. attempted is true
// before the physical send, including an uncertain partial transfer.
func (receipt *OriginalVMNeed) SendCheckpointLoanV1(ctx context.Context, connection net.Conn, owner *OwnedSupervisorLease, operation *SessionOperation, line *StartupLineV1, response proto.Response) (attempted bool, returned error) {
	if ctx == nil || receipt == nil || OriginalVMNeedFromContext(ctx) != receipt || owner == nil || owner.native == nil || operation == nil || line == nil || receipt.boot == nil || receipt.boot.owner != owner || receipt.need == nil || operation.native == nil || !operation.native.borrowed || !operation.Valid(owner.native.id) || !owner.Valid(operation.Primary()) || !owner.StartupCommittedV1() {
		return false, vmNeedError("the Snapshot loan has no original receipt, owner and borrowed outside operation")
	}
	if err := errors.Join(owner.native.rootRef.CheckRoot(operation.native.root), owner.CheckWarmSenderV1(line), receipt.CheckRunningNativeV1(ctx, connection)); err != nil {
		return false, err
	}
	var result proto.NativeCheckpointCustodyResultV1
	if response.ID != proto.NativeCheckpointCustodyRequestV1 || !response.OK || response.Error != nil || !api.ExactJSONFields(response.Result, result) || json.Unmarshal(response.Result, &result) != nil || result.Mode != "slot" || result.State.Primary != operation.Primary() || result.State.Generation != owner.generation || result.State.Need != startupDigest(receipt.rawNeed) {
		return false, vmNeedError("the original Snapshot response changed its closed role or runtime")
	}
	receipt.prepareMu.Lock()
	binding, need := receipt.binding, receipt.need
	receipt.prepareMu.Unlock()
	if binding == nil || need == nil {
		return false, vmNeedError("the original slot Snapshot binding ended")
	}
	returned = binding.WithCheckpointLoanV1(ctx, connection, *need, func(loan microvm.CheckpointLoanV1) error {
		return loan.WithLoanFilesV1(ctx, func(files []*os.File) error {
			if len(files) != proto.NativeCheckpointRoleCountV1 {
				return vmNeedError("the original slot Snapshot producer supplied another fixed role layout")
			}
			if err := errors.Join(owner.CheckWarmSenderV1(line), operation.native.check(ctx), receipt.CheckRunningNativeV1(ctx, connection)); err != nil {
				return err
			}
			raw, err := json.Marshal(response)
			if err != nil {
				return err
			}
			native, ok := line.conn.(*net.UnixConn)
			if !ok {
				return vmNeedError("the actual Snapshot loan lost its original native channel")
			}
			line.writeMu.Lock()
			attempted = true
			err = proto.SendUnixFrameV1(native, proto.TResponse, raw, files)
			line.writeMu.Unlock()
			return err
		})
	})
	return attempted, returned
}
