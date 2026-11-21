package client

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func armReceiptProjectionRowV2(t *testing.T, guest, trusted bool) map[string]any {
	t.Helper()
	row := receiptProjectionFixtureRow(t)
	receipt := row["delivery_receipt"].(map[string]any)
	receipt["version"], receipt["operation"] = 2, "arm"
	receipt["arm_supplied_names"], receipt["trusted_arm_supplied_names"] = []string{}, []string{}
	state := receipt["state"].(map[string]any)
	intent := api.SecretDeliveryIntent{Version: 1, Modes: map[string]api.SecretMode{"KEY": api.SecretModeExposed}, Binds: map[string]api.SecretBind{}}
	if guest {
		intent.Modes["KEY"] = api.SecretModeBound
		intent.Binds["KEY"] = api.SecretBind{Hosts: []string{"api.example.test:443"}, Header: "Authorization", Format: "Bearer {value}"}
		state["primary"] = map[string]api.PublicSecretState{"KEY": {Mode: api.SecretModeBound, Sentinel: "QWER1111", Identity: strings.Repeat("2", 32), Armed: true,
			Shape: &api.SecretShape{Version: 1, Slots: []api.SecretShapeSlot{{Class: "ascii-upper", Width: 1, Count: 4}, {Class: "ascii-digit", Width: 1, Count: 4}}}}}
		row["wall_intent"] = map[string]any{"level": "gvisor", "exposed": []string{}}
		receipt["arm_supplied_names"] = []string{"KEY"}
	} else {
		row["wall_intent"] = map[string]any{"level": "gvisor", "exposed": []string{"KEY"}}
	}
	row["delivery_intent"] = api.CloneSecretDeliveryIntent(intent)
	if trusted {
		intent.TrustedUses = map[string]api.TrustedSecretUseV1{"JUDGE_KEY": {Version: 1, Use: api.TrustedJudgeUseV1, Mode: api.SecretModeBound,
			Bind: api.SecretBind{Hosts: []string{"judge.example.test:443"}, Header: "Authorization", Format: "Bearer {value}"}, Model: "example-model", Endpoint: "https://judge.example.test/v1/chat/completions"}}
		receipt["trusted_arm_supplied_names"] = []string{"JUDGE_KEY"}
	}
	state["intent"] = intent
	return row
}

func TestArmReceiptV2GuestTrustedAndSeparateDomainsMatchFD(t *testing.T) {
	for _, modes := range []struct{ guest, trusted bool }{{true, false}, {false, true}, {true, true}} {
		row := armReceiptProjectionRowV2(t, modes.guest, modes.trusted)
		compareReceiptProjection(t, row, true)
		raw, _ := json.Marshal(row)
		decoded, err := decodeSandboxContext(t.Context(), projectionSession, raw, 4)
		if err != nil || decoded.deliveryReceipt == nil || decoded.deliveryReceipt.Version != 2 {
			t.Fatal("actual current arm receipt was not retained", err)
		}
		if !bytes.Equal(decoded.PolicySources, nil) || len(decoded.deliveryReceipt.Allowed) != 1 || decoded.deliveryReceipt.Allowed[0] != "KEY" {
			t.Fatal("trusted delivery expanded guest authority or rewrote the creation pin")
		}
		cloned := cloneSandboxDeliveryReceipt(decoded.deliveryReceipt)
		if len(cloned.TrustedArmSupplied) != 0 {
			cloned.TrustedArmSupplied[0] = "CHANGED"
			if decoded.deliveryReceipt.TrustedArmSupplied[0] != "JUDGE_KEY" {
				t.Fatal("an arm clone borrowed its mutable trusted delta")
			}
		}
	}
}

func TestArmReceiptV2RefusesNoopUnarmedAndForeignDeltas(t *testing.T) {
	for _, mutate := range []func(map[string]any){
		func(r map[string]any) { r["arm_supplied_names"] = []string{} },
		func(r map[string]any) { r["arm_supplied_names"] = nil },
		func(r map[string]any) { r["arm_supplied_names"] = []string{"OTHER"} },
		func(r map[string]any) { r["arm_supplied_names"] = []string{"KEY", "KEY"} },
		func(r map[string]any) { r["trusted_arm_supplied_names"] = []string{"OTHER"} },
		func(r map[string]any) { r["operation"] = "resume" },
		func(r map[string]any) {
			state := r["state"].(map[string]any)
			primary := state["primary"].(map[string]api.PublicSecretState)
			entry := primary["KEY"]
			entry.Armed = false
			primary["KEY"] = entry
		},
	} {
		row := armReceiptProjectionRowV2(t, true, false)
		mutate(row["delivery_receipt"].(map[string]any))
		compareReceiptProjection(t, row, false)
	}
	row := armReceiptProjectionRowV2(t, false, false)
	row["delivery_receipt"].(map[string]any)["arm_supplied_names"] = []string{"KEY"}
	compareReceiptProjection(t, row, false) // An exposed name cannot be armed.
}

func TestArmReceiptV2KeepsV1AndFutureSemanticsAndNeedsActualProof(t *testing.T) {
	row := receiptProjectionFixtureRow(t)
	row["delivery_receipt"].(map[string]any)["arm_supplied_names"] = 99
	compareReceiptProjection(t, row, true) // An unknown v1 extension stays opaque.
	row = receiptProjectionFixtureRow(t)
	row["delivery_receipt"].(map[string]any)["version"] = 4
	row["delivery_receipt"].(map[string]any)["state"] = "opaque future body"
	compareReceiptProjection(t, row, true)
	if err := storeArmDeliveryReceiptV2(t.Context(), nil, nil, "", &sandboxDeliveryReceipt{Version: 2, Operation: "arm"}, nil); err == nil {
		t.Fatal("a receipt DTO advanced without the original actual write and ACK proof")
	}
	for _, version := range []int{1, 2} {
		receipt := sandboxDeliveryReceipt{Version: version, ArmSupplied: []string{}, TrustedArmSupplied: []string{}}
		if version == 1 {
			receipt.ArmSupplied, receipt.TrustedArmSupplied = nil, nil
		}
		raw, err := json.Marshal(receipt)
		if err != nil || bytes.Contains(raw, []byte(`"arm_supplied_names"`)) != (version == 2) {
			t.Fatal("version-aware explicit empty delta arrays changed the v1 format", err)
		}
	}
}
