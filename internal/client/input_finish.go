package client

import (
	"context"
	"reflect"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/inputtransfer"
)

// HasPendingSessionInputReceipt is a local routing hint for an exact ID. It
// opens no connection and grants no permission to finish or resend inputs.
// A true result only selects the subsequent no-wake checked Finish path.
func HasPendingSessionInputReceipt(ctx context.Context, original Host, id string) (bool, error) {
	if err := bindRequestLocalSource(ctx, original); err != nil {
		return false, err
	}
	row, err := LoadSandboxPolicy(ctx, original, id)
	if err != nil {
		return false, err
	}
	if row == nil {
		return false, nil
	}
	if row.FutureLineage || row.FutureDelivery || row.futureInputOperation || row.futureDeliveryReceipt {
		return false, futureSandboxReceiptError()
	}
	return row.inputOperation != nil, ctx.Err()
}

// FinishSessionInputs completes only an original locally retained current-run
// input operation. It reads no laptop file, seed, environment or secret, and
// sends no payload. Callers dial without wake; no start/arm callback exists.
func (c *Conn) FinishSessionInputs(ctx context.Context, id string) error {
	ctx, _ = ensureGrantRequest(ctx)
	if !c.HasFeature(inputtransfer.FinishFeature) {
		return c.grantHelperOld("finish the original input operation without resending its files")
	}
	row, err := getSandboxReceiptPolicyContext(ctx, c, id)
	if err != nil {
		return err
	}
	if row == nil || row.FutureDelivery || row.FutureLineage || row.futureInputOperation || row.futureDeliveryReceipt || row.inputOperation == nil {
		return sandboxReceiptError()
	}
	receipt := cloneSandboxInputOperation(row.inputOperation)
	if err := validateSandboxInputOperation(ctx, receipt); err != nil {
		return err
	}
	pin, err := api.DecodePolicySourcePin(row.PolicySources)
	if err != nil || pin == nil || pin.ClientInputs == nil || pin.ClientInputs.Digest != receipt.SelectionHash {
		return policySourceStateError()
	}
	// The retained local pin provides only the old declaration's bounds.
	// It supplies no new path or input selection, even after a client restart.
	copyMax, seedMax := pin.ClientInputs.CopyMax, pin.ClientInputs.SeedMax
	w := &inputFinishWitness{conn: c, creation: row, receipt: receipt}
	operation := inputtransfer.OperationArgs{Scope: inputtransfer.Scope{Session: receipt.Scope.MemberID, Nonce: receipt.Nonce, Digest: receipt.Digest},
		RunGeneration: receipt.Scope.RunGeneration}
	var described inputtransfer.DescribeResult
	w.describeID, w.source, err = inputOperationRoundTrip(ctx, c, inputtransfer.Describe, operation, &described)
	if err != nil {
		return err
	}
	if w.describeID == 0 || described.Version != 1 || described.RunGeneration != operation.RunGeneration ||
		described.Summary.Nonce != receipt.Nonce || described.Summary.Digest != receipt.Digest {
		return sandboxReceiptError()
	}
	hash, err := sandboxInputSummaryHash(ctx, described.Summary, copyMax, seedMax)
	if err != nil {
		return err
	}
	if hash != receipt.SummaryHash {
		return sandboxReceiptError()
	}
	w.described = true
	var finished inputtransfer.BeginResult
	w.finishID, _, err = inputOperationRoundTrip(ctx, c, "inputs.begin", inputtransfer.BeginArgs{Scope: operation.Scope,
		Summary: &described.Summary, FinishOnly: true, RunGeneration: operation.RunGeneration}, &finished)
	if err != nil {
		return err
	}
	if w.finishID == 0 || finished.Version != 1 || !finished.Finished || finished.RunGeneration != operation.RunGeneration {
		return sandboxReceiptError()
	}
	w.finished = true
	return clearSandboxInputOperationContext(ctx, c, row, receipt, w)
}

// The actual raw connection owns one response and then exits. No generic
// reader, value getter, file/seed callback or new input plan can enter here.
func inputOperationRoundTrip(ctx context.Context, parent *Conn, op string, args, result any) (uint64, hostStateSourceWitness, error) {
	var source hostStateSourceWitness
	raw, err := dialInputContinuation(ctx, parent)
	if err != nil {
		return 0, source, err
	}
	defer raw.Close()
	defer raw.execReadFinished()
	carrier, err := newInputRawCarrier(raw)
	if err != nil {
		raw.kill()
		return 0, source, err
	}
	defer carrier.Close()
	closed := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(closed); _ = carrier.Close() })
	defer func() {
		if !stop() {
			<-closed
		}
	}()
	return inputContinuationRequest(ctx, parent, raw, op, args, result)
}

type inputFinishWitness struct {
	conn                 *Conn
	creation             *SandboxPolicy
	receipt              *sandboxInputOperation
	source               hostStateSourceWitness
	describeID, finishID uint64
	described, finished  bool
}

func (w *inputFinishWitness) checkSandboxReceiptWrite(ctx context.Context, intent *sandboxReceiptWriteIntent) error {
	if w == nil || intent == nil || intent.kind != inputOperationFinish || intent.conn != w.conn || !w.described || !w.finished ||
		w.describeID == 0 || w.finishID == 0 || w.receipt == nil || intent.expectedRevision != w.receipt.Revision ||
		!sameSandboxCreation(intent.original, w.creation) || !reflect.DeepEqual(intent.input, w.receipt) {
		return sandboxReceiptError()
	}
	if err := w.receipt.Scope.Source.check(ctx, w.conn); err != nil {
		return err
	}
	return w.source.check(ctx, w.conn)
}
