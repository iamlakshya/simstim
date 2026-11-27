package client

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/cachefs"
)

func TestSandboxReceiptCloneOwnsAllMutableMetadata(t *testing.T) {
	original := &SandboxPolicy{deliveryReceiptRaw: json.RawMessage(`{"future":[]}`), inputOperationRaw: json.RawMessage(`{"future":{}}`),
		deliveryReceipt: &sandboxDeliveryReceipt{Allowed: []string{"KEY"}, Supplied: []string{"KEY"}, Missing: []string{},
			State: &api.SessionSecretState{Intent: api.SecretDeliveryIntent{Modes: map[string]api.SecretMode{"KEY": api.SecretModeBound}},
				Primary: map[string]api.PublicSecretState{"KEY": {Shape: &api.SecretShape{Slots: []api.SecretShapeSlot{{Class: "lower", Count: 3}}}}}}},
		inputOperation: &sandboxInputOperation{Revision: "original"}}
	cloned := cloneSandboxPolicy(original)
	cloned.deliveryReceipt.Allowed[0] = "OTHER"
	cloned.deliveryReceipt.State.Intent.Modes["KEY"] = api.SecretModeExposed
	cloned.deliveryReceipt.State.Primary["KEY"].Shape.Slots[0].Count = 999
	cloned.deliveryReceiptRaw[0], cloned.inputOperationRaw[0] = '[', '['
	cloned.inputOperation.Revision = "changed"
	if original.deliveryReceipt.Allowed[0] != "KEY" || original.deliveryReceipt.State.Intent.Modes["KEY"] != api.SecretModeBound ||
		original.deliveryReceipt.State.Primary["KEY"].Shape.Slots[0].Count != 3 || original.deliveryReceiptRaw[0] != '{' ||
		original.inputOperationRaw[0] != '{' || original.inputOperation.Revision != "original" {
		t.Fatal("receipt clone borrowed mutable publication or input metadata")
	}
}

func TestSandboxReceiptFutureBodiesRemainOpaque(t *testing.T) {
	for _, key := range []string{"delivery_receipt", "input_operation"} {
		raw := []byte(`{"` + key + `": { "version": 2, "future": [ { "private-format": "unchanged" } ] }}`)
		fields, err := api.ProjectedJSONObjectFields(t.Context(), raw, key)
		if err != nil {
			t.Fatal(err)
		}
		row := &SandboxPolicy{}
		if key == "delivery_receipt" {
			row.deliveryReceiptRaw = fields[key]
		} else {
			row.inputOperationRaw = fields[key]
		}
		if err := decodeSandboxReceiptsContext(t.Context(), raw, 4, row); err != nil {
			t.Fatal(err)
		}
		if row.deliveryReceipt != nil || row.inputOperation != nil || !row.futureDeliveryReceipt && !row.futureInputOperation {
			t.Fatal("future receipt became a supported historical record")
		}
		cloned := cloneSandboxPolicy(row)
		if !bytes.Equal(cloned.deliveryReceiptRaw, row.deliveryReceiptRaw) || !bytes.Equal(cloned.inputOperationRaw, row.inputOperationRaw) {
			t.Fatal("opaque future receipt bytes changed")
		}
	}
}

func TestSandboxReceiptVersionAndAliasBarriers(t *testing.T) {
	for _, raw := range []string{
		`{"delivery_receipt":{"version":1,"Version":2}}`,
		`{"input_operation":{"version":1,"version":2}}`,
		`{"delivery_receipt":{"version":1e0}}`,
		`{"input_operation":{"version":0}}`,
		`{"delivery_receipt":{"version":1,"scope":{},"state":{}}}`,
		`{"input_operation":null}`,
	} {
		p, err := api.NewJSONBytesProjection(t.Context(), []byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		root, err := p.ObjectRoot()
		if err != nil {
			t.Fatal(err)
		}
		var span api.JSONSpan
		delivery := false
		err = p.Object(root, []string{"delivery_receipt", "input_operation"}, func(field api.JSONField) error {
			span, delivery = field.Value, field.Known == 0
			return nil
		})
		if err == nil {
			_, err = projectSandboxReceiptSpans(t.Context(), p, span, 4, delivery)
		}
		if err == nil {
			t.Fatalf("accepted malformed/ambiguous receipt %s", raw)
		}
	}
}

func TestSandboxReceiptStorageRequiresActualPrivateProof(t *testing.T) {
	ctx := context.Background()
	if err := storeSandboxDeliveryReceiptContext(ctx, nil, nil, "", nil, nil); err == nil {
		t.Fatal("a bare delivery DTO could enter the receipt writer")
	}
	if err := storeSandboxInputOperationContext(ctx, nil, nil, "", nil, nil); err == nil {
		t.Fatal("a bare input DTO could enter the receipt writer")
	}
	if err := clearSandboxInputOperationContext(ctx, nil, nil, nil, nil); err == nil {
		t.Fatal("an input nonce alone could clear the pending operation")
	}
}

func TestSandboxReceiptCursorCannotBroadenOriginalNames(t *testing.T) {
	for _, raw := range []string{
		`{"receipt":["KEY"],"original":["KEY"]}`,
		`{"receipt":[],"original":[]}`,
		`{"receipt":["KEY","OTHER"],"original":["KEY"]}`,
		`{"receipt":["KEY"],"original":["OTHER"]}`,
	} {
		p, err := api.NewJSONBytesProjection(t.Context(), []byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		root, err := p.ObjectRoot()
		if err != nil {
			t.Fatal(err)
		}
		var spans [2]api.JSONSpan
		err = p.Object(root, []string{"receipt", "original"}, func(field api.JSONField) error { spans[field.Known] = field.Value; return nil })
		if err != nil {
			t.Fatal(err)
		}
		err = validateSandboxReceiptAllowedSpans(t.Context(), p, spans[0], spans[1])
		want := raw == `{"receipt":["KEY"],"original":["KEY"]}` || raw == `{"receipt":[],"original":[]}`
		if (err == nil) != want {
			t.Fatalf("name-domain equality: got %v for %s", err, raw)
		}
	}
}

func TestSandboxInputReceiptCASAndExactFinishedClear(t *testing.T) {
	// Native reference is genuine historical metadata. This pure row callback
	// test does not construct a transport or claim publication/Finish proof.
	root, err := cachefs.OpenPrivateRoot(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	reference, err := cachefs.CaptureRoot(root)
	if closeErr := root.Close(); err != nil || closeErr != nil {
		t.Fatalf("capture native root: %v / %v", err, closeErr)
	}
	request := strings.Repeat("a", 32)
	row := &SandboxPolicy{SessionID: "store234", HostID: strings.Repeat("b", 32), Ceiling: TierOwn,
		CreatedAt: time.Unix(1, 0).UTC(), request: request, CreationWall: "process", Secrets: []string{}}
	raw, err := json.Marshal(savedSandbox{SessionID: row.SessionID, HostID: row.HostID, Ceiling: row.Ceiling.String(),
		CreatedAt: row.CreatedAt, Request: request, WallIntent: json.RawMessage(`{"level":"process"}`)})
	if err != nil {
		t.Fatal(err)
	}
	fields, err := sandboxObjectContext(t.Context(), raw)
	if err != nil {
		t.Fatal(err)
	}
	fields["future-row"] = json.RawMessage(`{ "untouched": [1, 2] }`)
	raw, err = marshalSandboxObject(fields)
	if err != nil {
		t.Fatal(err)
	}
	file := &sandboxFile{schema: 2, root: map[string]json.RawMessage{"schema": []byte("2")}, sessions: map[string]json.RawMessage{row.SessionID: raw}}
	operation := &sandboxInputOperation{Version: 1, Revision: strings.Repeat("c", 32), Creation: request, Operation: "new",
		Scope: sandboxReceiptScope{Host: "box", Source: hostStateSourceWitness{Version: 1, Exists: true, Generation: strings.Repeat("d", 32), Path: "/original/hosts.toml", Root: reference},
			HostID: row.HostID, MemberID: row.SessionID, RootID: row.SessionID, RunGeneration: strings.Repeat("e", 32)},
		Nonce: api.InputNonce(strings.Repeat("f", 64)), Digest: strings.Repeat("1", 64), SummaryHash: strings.Repeat("2", 64), SelectionHash: strings.Repeat("3", 64)}
	intent := &sandboxReceiptWriteIntent{kind: inputOperationBegin, original: row, input: operation}
	if err := changeSandboxReceiptRow(t.Context(), file, intent); err != nil {
		t.Fatal(err)
	}
	if file.schema != 4 || string(file.root["schema"]) != "4" {
		t.Fatal("input receipt missed the atomic schema barrier")
	}
	kept := bytes.Clone(file.sessions[row.SessionID])
	stale := cloneSandboxInputOperation(operation)
	stale.Revision = strings.Repeat("4", 32)
	if err := changeSandboxReceiptRow(t.Context(), file, &sandboxReceiptWriteIntent{kind: inputOperationBegin, original: row, input: stale}); err == nil || !bytes.Equal(kept, file.sessions[row.SessionID]) {
		t.Fatal("a stale publication replaced the winning input receipt")
	}
	wrong := cloneSandboxInputOperation(operation)
	wrong.Digest = strings.Repeat("5", 64)
	if err := changeSandboxReceiptRow(t.Context(), file, &sandboxReceiptWriteIntent{kind: inputOperationFinish, original: row, expectedRevision: operation.Revision, input: wrong}); err == nil {
		t.Fatal("a changed operation digest cleared the pending receipt")
	}
	if err := changeSandboxReceiptRow(t.Context(), file, &sandboxReceiptWriteIntent{kind: inputOperationFinish, original: row, expectedRevision: operation.Revision, input: operation}); err != nil {
		t.Fatal(err)
	}
	out, err := sandboxObjectContext(t.Context(), file.sessions[row.SessionID])
	if err != nil || out["input_operation"] != nil || !bytes.Equal(out["future-row"], fields["future-row"]) {
		t.Fatalf("exact clear changed unrelated metadata: %v", err)
	}
}

func TestSandboxInputSummaryHashBindsFactsBeyondDigestLiteral(t *testing.T) {
	summary := api.InputSummary{Version: api.SessionInputsV1, Nonce: api.InputNonce(strings.Repeat("a", 64)), Digest: strings.Repeat("b", 64), FileCount: 1, CopyBytes: 1}
	first, err := sandboxInputSummaryHash(t.Context(), summary, 1024, 1024)
	if err != nil {
		t.Fatal(err)
	}
	summary.CopyBytes = 2 // The claimed manifest digest is deliberately unchanged.
	second, err := sandboxInputSummaryHash(t.Context(), summary, 1024, 1024)
	if err != nil || first == second {
		t.Fatalf("full-summary hash ignored changed counts behind copied Digest: %v", err)
	}
}
