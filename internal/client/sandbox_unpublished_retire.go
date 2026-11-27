package client

import (
	"context"
	"errors"
	"io"
	"reflect"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
)

// Main's actual terminal plan owns this lease. Under its original plan mutex
// it verifies the SAME request/Conn/reservation, freezes terminal abort and
// proves no underlying writer was EVER attempted. Future attempts must remain
// blocked through release. Release drops only this lease; an uncertain result
// must retain the actual witness/captures for caller-owned cleanup/recovery.
type unpublishedSandboxRetirementProof interface {
	holdUnpublishedSandboxRetirement(context.Context, *Conn, *SandboxPolicy) (release func(), err error)
}

// retireUnpublishedSandboxContext removes only one exact, known prospective
// reservation. It proves no remote absence/completion and sends no operation.
// retired reports an actual committed row replacement, even when subsequent
// flush/close/cancellation checks fail. Such failure remains incomplete.
func retireUnpublishedSandboxContext(ctx context.Context, c *Conn, original *SandboxPolicy, proof unpublishedSandboxRetirementProof) (retired bool, failure error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	frozen := cloneSandboxPolicy(original)
	if proof == nil || c == nil || frozen == nil || frozen.HostID != c.HostID() {
		return false, sandboxReceiptError()
	}
	if err := validateUnpublishedSandboxSnapshot(frozen); err != nil {
		return false, err
	}
	release, err := proof.holdUnpublishedSandboxRetirement(ctx, c, frozen)
	if err != nil {
		return false, err
	}
	if release == nil {
		return false, sandboxReceiptError()
	}
	defer release()
	return retireProspectiveSandboxLockedV1(ctx, c, frozen)
}

// Caller holds one of the two concrete original retirement leases. Both keep
// their distinct publication proofs while sharing this same protected CAS.
func retireProspectiveSandboxLockedV1(ctx context.Context, c *Conn, frozen *SandboxPolicy) (retired bool, failure error) {
	failure = withSandboxReceiptObservation(ctx, c, func(owned context.Context) error {
		source, err := captureHostStateSourceWitness(owned, c)
		if err != nil {
			return err
		}
		err = withStateTransaction(owned, []string{"hosts", HostFileKey(c.Host.Name)}, "sandboxes.json", false, true, func(tx *stateFileTxn) error {
			view := tx.view()
			if !view.Exists {
				return sandboxReceiptError()
			}
			raw, err := io.ReadAll(io.NewSectionReader(view.ReaderAt, 0, view.Size))
			if err != nil {
				return wrapGrantStateError("sandboxes.json", err)
			}
			encoded, err := changeSandboxFileContext(tx.ctx, c.Host, raw, true, true, func(file *sandboxFile) error {
				return retireUnpublishedSandboxRow(tx.ctx, file, frozen)
			})
			if err != nil {
				return err
			}
			// All owner/source/native guards come from the SAME actual lower
			// transaction. No outer-context membership lookup in this callback.
			if err := c.hostTransport.checkConn(c); err != nil {
				return err
			}
			err = tx.replace(func(writeCtx context.Context, out io.Writer) error {
				if err := writeCtx.Err(); err != nil {
					return err
				}
				_, err := out.Write(encoded)
				return wrapGrantStateError("sandboxes.json", err)
			})
			retired = tx.output != nil
			var callback *stateCallbackError
			if errors.As(err, &callback) && err == callback {
				return callback.err
			}
			return wrapGrantStateError("sandboxes.json", err)
		})
		if err != nil {
			return err
		}
		return source.check(owned, c)
	})
	return retired, failure
}

func validateUnpublishedSandboxSnapshot(p *SandboxPolicy) error {
	if p == nil || !api.ValidSessionID(p.SessionID) || !ValidHostID(p.HostID) || !ValidHostID(p.request) || !p.Ceiling.Valid() || p.CreatedAt.IsZero() ||
		p.FutureLineage || p.FutureDelivery || p.futureDeliveryReceipt || p.futureInputOperation ||
		p.deliveryReceipt != nil || p.inputOperation != nil || p.deliveryReceiptRaw != nil || p.inputOperationRaw != nil {
		return sandboxReceiptError()
	}
	if !api.ExactJSONFields(p.wallIntent, (*savedCreationIntent)(nil)) ||
		p.lineageRaw != nil && !api.ExactJSONFields(p.lineageRaw, (*sandboxLineage)(nil)) ||
		p.deliveryRaw != nil && !api.ExactJSONFields(p.deliveryRaw, (*api.SecretDeliveryIntent)(nil)) {
		return futureSandboxReceiptError()
	}
	return nil
}

func retireUnpublishedSandboxRow(ctx context.Context, file *sandboxFile, original *SandboxPolicy) error {
	if file.schema != 4 {
		return sandboxReceiptError()
	}
	raw, exists := file.sessions[original.SessionID]
	if !exists {
		return sandboxReceiptError()
	}
	// Derive the exact top-level field set from the sole saved-row DTO.
	// Unknown metadata is retained, never deleted as an owned reservation.
	known := api.ExactJSONFieldNames((*savedSandbox)(nil))
	fields, err := sandboxObjectContext(ctx, raw, known...)
	if err != nil {
		return err
	}
	for key := range fields {
		if _, ok := slices.BinarySearch(known, key); !ok {
			return futureSandboxReceiptError()
		}
	}
	current, err := decodeSandboxContext(ctx, original.SessionID, raw, file.schema)
	if err != nil {
		return err
	}
	if err := validateUnpublishedSandboxSnapshot(&current); err != nil {
		return err
	}
	if !sameSandboxCreation(&current, original) || current.Ceiling != original.Ceiling || !slices.Equal(current.Secrets, original.Secrets) ||
		!reflect.DeepEqual(current.DeliveryIntent, original.DeliveryIntent) {
		return sandboxReceiptError()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	delete(file.sessions, original.SessionID)
	return nil
}
