package client

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/proto"
)

func TestRuntimeUpgradeRequiresOriginalRequestBeforeRPC(t *testing.T) {
	conn := &Conn{}
	if _, err := UpgradeRuntimeOnUse(t.Context(), conn, RuntimeUpgradeOptions{}); err == nil || conn.nextID.Load() != 0 {
		t.Fatal("standalone upgrade invented a request or sent a frame", err)
	}
}

func TestRuntimeUpgradeSharedConfirmationRetainsOriginalSettings(t *testing.T) {
	h := h5GrantState(t)
	if err := SetTierContext(t.Context(), h.Name, TierShared, strings.Repeat("a", 32), ""); err != nil {
		t.Fatal(err)
	}
	manifest := runtimeDeliveryManifest(t, []byte("checked runtime"))
	useRuntimeDeliveryManifest(t, manifest)
	c := finishGeneratedTransportFixture(t, h, strings.Repeat("a", 32), "sandbox")
	c.pending = map[uint64]chan proto.Response{}
	calls := 0
	sharedGrantWire(t, c, func(request proto.Request) any {
		calls++
		if request.Op != "runtime.status" {
			t.Fatal("shared host received a mutation before confirmation", request.Op)
		}
		return runtimeDeliveryStatus(manifest, false)
	})
	r := NewGrantRequest(false, false)
	ctx := WithGrantRequest(t.Context(), r)
	defer r.Close()
	_, err := UpgradeRuntimeOnUse(ctx, c, RuntimeUpgradeOptions{RuntimeOptions{Probe: runtimeDeliveryProbe(false)}})
	var question *ConfirmError
	if !errors.As(err, &question) || calls != 1 || r.preparationSettings == nil {
		t.Fatal("shared upgrade lost its original scoped question or settings owner", err, calls)
	}
	original := r.preparationSettings
	confirmed, err := ConfirmGrant(ctx, err)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath(), []byte("[images]\nfetch='host'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := UpgradeRuntimeOnUse(confirmed, c, RuntimeUpgradeOptions{RuntimeOptions{Probe: runtimeDeliveryProbe(false)}}); err == nil || calls != 1 || r.preparationSettings != original {
		t.Fatal("confirmed retry adopted replacement settings or sent another frame", err, calls)
	}
}

func TestRuntimeUpgradeQuestionCannotApproveChangedSignedRuntime(t *testing.T) {
	h := h5GrantState(t)
	if err := SetTierContext(t.Context(), h.Name, TierShared, strings.Repeat("a", 32), ""); err != nil {
		t.Fatal(err)
	}
	manifest := runtimeDeliveryManifest(t, []byte("first checked runtime"))
	useRuntimeDeliveryManifest(t, manifest)
	c := finishGeneratedTransportFixture(t, h, strings.Repeat("a", 32), "sandbox")
	c.pending = map[uint64]chan proto.Response{}
	calls := 0
	sharedGrantWire(t, c, func(request proto.Request) any {
		calls++
		if request.Op != "runtime.status" {
			t.Fatal("mutation preceded scoped consent", request.Op)
		}
		return runtimeDeliveryStatus(manifest, false)
	})
	r := NewRuntimeUpgradeGrantRequest()
	ctx := WithGrantRequest(t.Context(), r)
	defer r.Close()
	_, question := UpgradeRuntimeOnUse(ctx, c, RuntimeUpgradeOptions{RuntimeOptions{Probe: runtimeDeliveryProbe(false)}})
	confirmed, err := ConfirmGrant(ctx, question)
	if err != nil {
		t.Fatal(err)
	}
	useRuntimeDeliveryManifest(t, runtimeDeliveryManifest(t, []byte("replacement checked runtime")))
	if _, err := UpgradeRuntimeOnUse(confirmed, c, RuntimeUpgradeOptions{RuntimeOptions{Probe: runtimeDeliveryProbe(false)}}); err == nil || calls != 1 {
		t.Fatal("a previous answer approved changed signed bytes", err, calls)
	}
}
