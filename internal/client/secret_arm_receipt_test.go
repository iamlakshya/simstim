package client

import (
	"bytes"
	"encoding/hex"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestActualArmACKStoresVersionTwoAndOnlyItsRealDelta(t *testing.T) {
	f := newArmSourceFixtureV1(t, false)
	state, err := f.conn.ArmSecrets(f.ctx, "arm23456", false)
	if err != nil {
		t.Fatal(err)
	}
	row, err := getSandboxReceiptPolicyContext(f.ctx, f.conn, "arm23456")
	if err != nil || row == nil || row.deliveryReceipt == nil {
		t.Fatal("actual correlated arm ACK has no local receipt", err)
	}
	receipt := row.deliveryReceipt
	if receipt.Version != 2 || receipt.Operation != "arm" || receipt.Scope.BackendGeneration != state.Generation || receipt.Scope.BackendGeneration == strings.Repeat("d", 32) || !reflect.DeepEqual(receipt.Allowed, []string{"KEY"}) || !reflect.DeepEqual(receipt.Supplied, []string{"KEY"}) || !reflect.DeepEqual(receipt.Missing, []string{}) || !reflect.DeepEqual(receipt.ArmSupplied, []string{"KEY"}) || !reflect.DeepEqual(receipt.TrustedArmSupplied, []string{}) {
		t.Fatal("arm receipt changed the original domain or lost the actual delta/generation")
	}
	f.request.mu.Lock()
	p := f.request.armSecrets
	f.request.mu.Unlock()
	raw, err := os.ReadFile(sandboxStatePath(f.conn.Host))
	if err != nil || bytes.Contains(raw, []byte("ABCD0000")) || bytes.Contains(raw, []byte(hex.EncodeToString(p.wire[:]))) {
		t.Fatal("receipt persisted a real value or private wire hash", err)
	}
	if err := p.storeAcknowledgedReceiptV1(f.ctx, f.conn, state); err != nil {
		t.Fatal("same actual ACK proof could not finish its identical local receipt", err)
	}
	again, err := getSandboxReceiptPolicyContext(f.ctx, f.conn, "arm23456")
	if err != nil || !reflect.DeepEqual(again.deliveryReceipt, receipt) || f.armFrames.Load() != 1 {
		t.Fatal("receipt retry changed its revision or replayed values", err)
	}
}

func TestReadOnlyAlreadyArmedObservationWritesNoReceipt(t *testing.T) {
	f := newArmSourceFixtureV1(t, false)
	f.mu.Lock()
	public := f.cohort.SecretState.Primary["KEY"]
	public.Armed = true
	f.cohort.SecretState.Primary["KEY"] = public
	f.mu.Unlock()
	if _, err := f.conn.ArmSecrets(f.ctx, "arm23456", false); err != nil {
		t.Fatal(err)
	}
	row, err := LoadSandboxPolicy(f.ctx, f.conn.Host, "arm23456")
	if err != nil || row == nil || row.deliveryReceipt != nil || f.armFrames.Load() != 0 {
		t.Fatal("read-only readiness manufactured an arm publication receipt", err)
	}
	if _, err := os.Stat(f.marker); !os.IsNotExist(err) {
		t.Fatal("already armed observation fetched a value", err)
	}
}

func TestMismatchedArmACKCannotAdvanceReceiptOrReplay(t *testing.T) {
	f := newArmSourceFixtureV1(t, false)
	f.mismatchAck = true
	if _, err := f.conn.ArmSecrets(f.ctx, "arm23456", false); err == nil {
		t.Fatal("another request's acknowledgement became arm evidence")
	}
	row, err := LoadSandboxPolicy(f.ctx, f.conn.Host, "arm23456")
	if err != nil || row == nil || row.deliveryReceipt != nil {
		t.Fatal("mismatched acknowledgement advanced the local receipt", err)
	}
	if _, err := f.conn.ArmSecrets(f.ctx, "arm23456", false); err == nil || f.armFrames.Load() != 1 {
		t.Fatal("ignored mismatched ACK replayed credentials", err)
	}
}

func TestActualDualRoleArmStoresSeparateDeltasWithoutAnotherFetch(t *testing.T) {
	f := newArmSourceFixtureV1(t, false)
	writeRetainedImagePolicyFixture(t, f.config+"\n[classifier]\njudge='model'\nmodel='example-model'\nendpoint='https://model.example/v1/chat/completions'\nkey='KEY'\n")
	settings, err := ReadPolicySettings()
	if err != nil {
		t.Fatal(err)
	}
	layers, err := ResolvePolicyLayers(settings, f.conn.Host, "", "", api.LayerValues{})
	if err != nil {
		t.Fatal(err)
	}
	f.policy, _, err = egress.MergeLayers(layers)
	if err != nil {
		t.Fatal(err)
	}
	state, err := f.conn.ArmSecrets(f.ctx, "arm23456", false)
	if err != nil {
		t.Fatal(err)
	}
	row, err := getSandboxReceiptPolicyContext(f.ctx, f.conn, "arm23456")
	if err != nil || row == nil || row.deliveryReceipt == nil {
		t.Fatal("actual dual-role ACK has no receipt", err)
	}
	receipt := row.deliveryReceipt
	if !reflect.DeepEqual(receipt.Allowed, []string{"KEY"}) || !reflect.DeepEqual(receipt.ArmSupplied, []string{"KEY"}) || !reflect.DeepEqual(receipt.TrustedArmSupplied, []string{"KEY"}) || !reflect.DeepEqual(receipt.State.Intent.TrustedUses, state.Intent.TrustedUses) {
		t.Fatal("trusted delta replaced guest authority or lost its own role")
	}
	count, err := os.ReadFile(f.marker)
	if err != nil || string(count) != "x" || f.armFrames.Load() != 1 {
		t.Fatal("dual-role arm fetched or published a second value", err)
	}
}

func TestActualArmACKCannotPublishReceiptAfterSourceOrFileReplacement(t *testing.T) {
	for _, mode := range []string{"settings", "file"} {
		t.Run(mode, func(t *testing.T) {
			f := newArmSourceFixtureV1(t, false)
			f.onArmAck = func() {
				if mode == "settings" {
					writeRetainedImagePolicyFixture(t, "# source changed after value publication\n")
					return
				}
				if err := os.Rename(sandboxStatePath(f.conn.Host), filepath.Join(t.TempDir(), "original-state.json")); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.conn.ArmSecrets(f.ctx, "arm23456", false); err == nil {
				t.Fatal("stale source or replaced file advanced an arm receipt")
			}
			if _, err := f.conn.ArmSecrets(f.ctx, "arm23456", false); err == nil || f.armFrames.Load() != 1 {
				t.Fatal("stale receipt failure replayed a credential frame", err)
			}
			if mode == "file" {
				if _, err := os.Stat(sandboxStatePath(f.conn.Host)); !os.IsNotExist(err) {
					t.Fatal("receipt writer recreated a missing original file", err)
				}
			}
		})
	}
}
