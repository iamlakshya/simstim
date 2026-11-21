package client

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestRewindReceiptV3UsesOneCanonicalTypedAndFDGrammar(t *testing.T) {
	row := receiptProjectionFixtureRow(t)
	receipt := row["delivery_receipt"].(map[string]any)
	receipt["version"], receipt["operation"] = 3, "rewind"
	compareReceiptProjection(t, row, true)
	raw, _ := json.Marshal(row)
	decoded, err := decodeSandboxContext(t.Context(), projectionSession, raw, 4)
	if err != nil || decoded.deliveryReceipt == nil || decoded.deliveryReceipt.Version != 3 || decoded.deliveryReceipt.Operation != "rewind" {
		t.Fatal("the actual v3 receipt was not retained", err)
	}
	for _, operation := range []string{"new", "resume", "spawn", "arm", "unknown"} {
		row := receiptProjectionFixtureRow(t)
		receipt := row["delivery_receipt"].(map[string]any)
		receipt["version"], receipt["operation"] = 3, operation
		compareReceiptProjection(t, row, false)
	}
	for _, value := range []any{[]string{}, []string{"KEY"}, 2} {
		row := receiptProjectionFixtureRow(t)
		receipt := row["delivery_receipt"].(map[string]any)
		receipt["version"], receipt["operation"], receipt["arm_supplied_names"] = 3, "rewind", value
		compareReceiptProjection(t, row, false)
	}
	row = receiptProjectionFixtureRow(t)
	receipt = row["delivery_receipt"].(map[string]any)
	receipt["version"], receipt["operation"], receipt["arm_supplied_names"], receipt["state"] = 3, "rewind", []string{}, map[string]any{"version": 99}
	compareReceiptProjection(t, row, false)
	row = receiptProjectionFixtureRow(t)
	receipt = row["delivery_receipt"].(map[string]any)
	receipt["version"], receipt["operation"], receipt["arm_supplied_names"] = 3, "rewind", nil
	compareReceiptProjection(t, row, true)
	row = receiptProjectionFixtureRow(t)
	receipt = row["delivery_receipt"].(map[string]any)
	receipt["version"], receipt["state"] = 4, "opaque later state"
	compareReceiptProjection(t, row, true)
	row = receiptProjectionFixtureRow(t)
	receipt = row["delivery_receipt"].(map[string]any)
	receipt["version"], receipt["operation"], receipt["future_extension"] = 3, "rewind", true
	compareReceiptProjection(t, row, true)
	raw, _ = json.Marshal(row)
	decoded, err = decodeSandboxContext(t.Context(), projectionSession, raw, 4)
	if err != nil || !decoded.futureDeliveryReceipt || decoded.deliveryReceipt != nil {
		t.Fatal("an extended v3 receipt became current authority", err)
	}
	if err := storeRewindDeliveryReceiptsV3(t.Context(), nil, nil, nil, nil); err == nil {
		t.Fatal("receipt data wrote without an actual private commit/ACK witness")
	}
}

func TestRewindReceiptV3BatchLateCASFailurePublishesNoPartialMember(t *testing.T) {
	c, _, ctx, selection, _, commits := rewindClientFixtureV1(t, false)
	p, err := c.PrepareRewind(ctx, selection, RewindOptions{})
	if err != nil {
		t.Fatal(err)
	}
	owned, finish, err := p.begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	base := owned
	owned, args, err := p.arguments(owned)
	if err != nil {
		t.Fatal(err)
	}
	publication, release, err := c.beginGrant(owned, "actual rewind publication fixture", false)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	w, err := p.newPublicationWitnessV3(base, publication)
	if err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.publication = w
	p.mu.Unlock()
	var result api.RewindResult
	if err := c.callWithSent(publication, api.OpRewindCommitV1, args, &result, release); err != nil {
		t.Fatal(err)
	}
	if !w.hasAcknowledgementV3() || *commits != 1 {
		t.Fatal("fixture did not use the actual correlated writer and response")
	}
	if err := w.restrictAcknowledgementV3(base); err != nil {
		t.Fatal(err)
	}
	if err := w.prepareReceiptsV3(base); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(StateDir(), "hosts", HostFileKey(c.Host.Name), "sandboxes.json")
	original, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(original, &root) != nil {
		t.Fatal("invalid fixture ledger")
	}
	var rows map[string]json.RawMessage
	if json.Unmarshal(root["sessions"], &rows) != nil {
		t.Fatal("invalid fixture rows")
	}
	var child map[string]json.RawMessage
	if json.Unmarshal(rows[selection.ID], &child) != nil {
		t.Fatal("invalid fixture child")
	}
	concurrent := cloneSandboxDeliveryReceipt(w.receipts[selection.ID])
	concurrent.Revision = strings.Repeat("1", 32)
	child["delivery_receipt"], err = json.Marshal(concurrent)
	if err != nil {
		t.Fatal(err)
	}
	rows[selection.ID], _ = json.Marshal(child)
	root["sessions"], _ = json.Marshal(rows)
	root["schema"] = json.RawMessage("4")
	interleaved, _ := json.Marshal(root)
	// An adversarial local CAS replacement exercises the native batch writer;
	// this byte mutation issues no publication/fetch permission.
	if err := os.WriteFile(file, interleaved, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := w.finishAcknowledgedV3(base); err == nil {
		t.Fatal("the late child revision was overwritten")
	}
	after, err := os.ReadFile(file)
	if err != nil || !bytes.Equal(after, interleaved) {
		t.Fatal("a late child failure published the primary row separately", err)
	}
	if err := os.WriteFile(file, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := w.finishAcknowledgedV3(base); err != nil || *commits != 1 {
		t.Fatal("the original local ACK could not retry without replaying restore", err)
	}
}

func TestRewindReceiptV3RequiresActualFreshCorrelatedPublication(t *testing.T) {
	for name, mutate := range map[string]func(*api.RewindResult){
		"missing run":     func(r *api.RewindResult) { r.Session.RunGeneration = "" },
		"old run":         func(r *api.RewindResult) { r.Session.RunGeneration = strings.Repeat("b", 32) },
		"old backend":     func(r *api.RewindResult) { r.Session.SecretState.Generation = strings.Repeat("c", 32) },
		"missing state":   func(r *api.RewindResult) { r.Session.SecretState = nil },
		"another root":    func(r *api.RewindResult) { r.Session.SandboxOf = "foreign2" },
		"foreign backend": func(r *api.RewindResult) { r.Session.SecretState.Generation = strings.Repeat("f", 32) },
	} {
		t.Run(name, func(t *testing.T) {
			c, _, ctx, selection, _, _ := rewindClientFixtureV1(t, false, mutate)
			p, err := c.PrepareRewind(ctx, selection, RewindOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := p.Commit(ctx); err == nil {
				t.Fatal("an uncorrelated or stale publication became fresh local authority")
			}
			for id := range p.group.members {
				row, err := LoadSandboxPolicy(ctx, c.Host, id)
				if err != nil || row == nil || row.deliveryReceipt != nil {
					t.Fatal("a refused publication advanced a member receipt", err)
				}
			}
		})
	}
}
