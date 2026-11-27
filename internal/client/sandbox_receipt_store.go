package client

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"slices"
)

type sandboxReceiptWriteKind uint8

const (
	deliveryPublicationReceipt sandboxReceiptWriteKind = iota + 1
	inputOperationBegin
	inputOperationFinish
	armPublicationReceipt
	rewindPublicationReceipt
)

type sandboxReceiptWriteIntent struct {
	kind             sandboxReceiptWriteKind
	conn             *Conn
	original         *SandboxPolicy
	expectedRevision string
	delivery         *sandboxDeliveryReceipt
	input            *sandboxInputOperation
}

// Only Main's actual outgoing publication/input witness implements this.
// A DTO, saved row, cohort or public bool cannot satisfy the private method.
// Implementations must not retain or mutate the borrowed immutable intent.
type sandboxReceiptWriteProof interface {
	checkSandboxReceiptWrite(context.Context, *sandboxReceiptWriteIntent) error
}

func storeSandboxDeliveryReceiptContext(ctx context.Context, c *Conn, original *SandboxPolicy, expectedRevision string, next *sandboxDeliveryReceipt, proof sandboxReceiptWriteProof) error {
	return writeSandboxReceiptContext(ctx, sandboxReceiptWriteIntent{kind: deliveryPublicationReceipt, conn: c, original: cloneSandboxPolicy(original),
		expectedRevision: expectedRevision, delivery: cloneSandboxDeliveryReceipt(next)}, proof)
}

func storeSandboxInputOperationContext(ctx context.Context, c *Conn, original *SandboxPolicy, expectedRevision string, next *sandboxInputOperation, proof sandboxReceiptWriteProof) error {
	return writeSandboxReceiptContext(ctx, sandboxReceiptWriteIntent{kind: inputOperationBegin, conn: c, original: cloneSandboxPolicy(original),
		expectedRevision: expectedRevision, input: cloneSandboxInputOperation(next)}, proof)
}

// expected is the complete original tiny receipt, not only its nonce. Main's
// proof additionally requires the exact successful finished ACK of that run.
func clearSandboxInputOperationContext(ctx context.Context, c *Conn, original *SandboxPolicy, expected *sandboxInputOperation, proof sandboxReceiptWriteProof) error {
	intent := sandboxReceiptWriteIntent{kind: inputOperationFinish, conn: c, original: cloneSandboxPolicy(original), input: cloneSandboxInputOperation(expected)}
	if expected != nil {
		intent.expectedRevision = expected.Revision
	}
	return writeSandboxReceiptContext(ctx, intent, proof)
}

func writeSandboxReceiptContext(ctx context.Context, intent sandboxReceiptWriteIntent, proof sandboxReceiptWriteProof) error {
	if proof == nil || intent.original == nil || intent.conn == nil || intent.expectedRevision != "" && !ValidHostID(intent.expectedRevision) {
		return sandboxReceiptError()
	}
	return withSandboxReceiptObservation(ctx, intent.conn, func(owned context.Context) error {
		if err := validateSandboxReceiptWriteIntent(owned, &intent); err != nil {
			return err
		}
		// This is before the lower member/byte callback. It may inspect
		// Main's actual pending-request witness and original request lock.
		if err := proof.checkSandboxReceiptWrite(owned, &intent); err != nil {
			return err
		}
		err := withSandboxFile(owned, intent.conn.Host, true, func(file *sandboxFile) error {
			return changeSandboxReceiptRow(owned, file, &intent)
		})
		if err != nil {
			return err
		}
		// The committed metadata is retained on cancellation or uncertainty.
		// A failed final check must never trigger a resend or a guessed clear.
		if err := validateSandboxReceiptWriteIntent(owned, &intent); err != nil {
			return err
		}
		return proof.checkSandboxReceiptWrite(owned, &intent)
	})
}

func validateSandboxReceiptWriteIntent(ctx context.Context, intent *sandboxReceiptWriteIntent) error {
	p, c := intent.original, intent.conn
	if p == nil || c == nil || p.request == "" || p.SessionID == "" || p.HostID != c.HostID() || p.FutureLineage || p.FutureDelivery {
		return sandboxReceiptError()
	}
	var scope sandboxReceiptScope
	var creation, previous string
	switch intent.kind {
	case deliveryPublicationReceipt, armPublicationReceipt, rewindPublicationReceipt:
		if intent.delivery == nil || intent.kind == armPublicationReceipt && (intent.delivery.Version != 2 || intent.delivery.Operation != "arm") || intent.kind == rewindPublicationReceipt && (intent.delivery.Version != 3 || intent.delivery.Operation != "rewind") || intent.kind == deliveryPublicationReceipt && intent.delivery.Version != 1 {
			return sandboxReceiptError()
		}
		if err := validateSandboxDeliveryReceipt(ctx, intent.delivery); err != nil {
			return err
		}
		if !slices.Equal(intent.delivery.Allowed, localSandboxNames(p)) || !sandboxReceiptMatchesDelivery(p, intent.delivery) {
			return sandboxReceiptError()
		}
		scope, creation, previous = intent.delivery.Scope, intent.delivery.Creation, intent.delivery.Previous
	case inputOperationBegin, inputOperationFinish:
		if err := validateSandboxInputOperation(ctx, intent.input); err != nil {
			return err
		}
		scope, creation, previous = intent.input.Scope, intent.input.Creation, intent.input.Previous
	default:
		return sandboxReceiptError()
	}
	if scope.Host != c.Host.Name || !scope.matchesPolicy(p) || creation != p.request ||
		intent.kind != inputOperationFinish && previous != intent.expectedRevision ||
		intent.kind == inputOperationFinish && intent.input.Revision != intent.expectedRevision {
		return sandboxReceiptError()
	}
	return scope.Source.check(ctx, c)
}

func changeSandboxReceiptRow(ctx context.Context, file *sandboxFile, intent *sandboxReceiptWriteIntent) error {
	if file.schema != 2 && file.schema != 3 && file.schema != 4 {
		return sandboxReceiptError()
	}
	beforeRaw, ok := file.sessions[intent.original.SessionID]
	if !ok {
		return sandboxReceiptError()
	}
	before, err := decodeSandboxContext(ctx, intent.original.SessionID, beforeRaw, file.schema)
	if err != nil {
		return err
	}
	if !sameSandboxCreation(&before, intent.original) {
		return sandboxReceiptError()
	}
	key := "input_operation"
	var current string
	var next any = intent.input
	if intent.kind == deliveryPublicationReceipt || intent.kind == armPublicationReceipt || intent.kind == rewindPublicationReceipt {
		key, next = "delivery_receipt", intent.delivery
		if before.futureDeliveryReceipt {
			return futureSandboxReceiptError()
		}
		if before.deliveryReceipt != nil {
			current = before.deliveryReceipt.Revision
		}
	} else {
		if before.futureInputOperation {
			return futureSandboxReceiptError()
		}
		if before.inputOperation != nil {
			current = before.inputOperation.Revision
		}
	}
	if current != intent.expectedRevision {
		if (intent.kind == armPublicationReceipt || intent.kind == rewindPublicationReceipt) && before.deliveryReceipt != nil && reflect.DeepEqual(before.deliveryReceipt, intent.delivery) {
			return errStateNoChange // Only the same actual private ACK proof may retry.
		}
		return sandboxReceiptError()
	}
	if intent.kind == inputOperationFinish && !reflect.DeepEqual(before.inputOperation, intent.input) {
		return sandboxReceiptError()
	}
	fields, err := sandboxObjectContext(ctx, beforeRaw)
	if err != nil {
		return err
	}
	if intent.kind == inputOperationFinish {
		delete(fields, key)
	} else {
		encoded, err := json.Marshal(next)
		if err != nil {
			return err
		}
		if bytes.Equal(fields[key], encoded) {
			return errStateNoChange
		}
		fields[key] = encoded
	}
	encoded, err := marshalSandboxObject(fields)
	if err != nil {
		return err
	}
	if _, err := decodeSandboxContext(ctx, before.SessionID, encoded, 4); err != nil {
		return err
	}
	if err := checkSandboxLineageSchemaPromotion(ctx, file); err != nil {
		return err
	}
	file.schema, file.root["schema"] = 4, json.RawMessage("4")
	file.sessions[before.SessionID] = encoded
	return ctx.Err()
}
