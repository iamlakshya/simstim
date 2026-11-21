//go:build linux

package microvm

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/slotlaunch"
	"github.com/iamlakshya/simstim/internal/vmneed"
)

func TestAccountDisplayCannotUsePreparedModeOrZeroNativeIssuer(t *testing.T) {
	spec := firecrackerFixture(t)
	expected := vmneed.NeedV1{BootV1: spec.VM.Clone(), Driver: "firecracker", CPUs: spec.CPUs, DiskMax: spec.DiskMax}
	one, two := net.Pipe()
	defer one.Close()
	defer two.Close()
	connection := &ownedGuestLink{Conn: one}
	proof := &slotNativeProof{ready: true, source: &slotlaunch.NativeSourceV1{}, spec: spec, guest: connection}
	s := &sandbox{driver: &Driver{kind: firecrackerBackend}, spec: spec, uid: slotUIDModeV1Name, owner: t.Context(),
		started: true, helloChecked: true, connection: connection, control: &runnerControl{native: proof}}
	binding := &NativeBindingV1{verifier: &slotSandboxBinding{sandbox: s, connection: connection, proof: proof}}
	for _, actual := range []net.Conn{connection, &ownedGuestLink{Conn: one}, two} {
		if own, err := OriginalAccountModeV1(t.Context(), s, actual, binding, expected); own || err == nil {
			t.Fatal("prepared fields, another wrapper or zero native source issued an own-account label")
		}
	}
	if own, err := OriginalAccountModeV1(context.Background(), s, connection, &NativeBindingV1{}, expected); own || err == nil {
		t.Fatal("a zero native binding issued an own-account label")
	}
}

func TestAccountDisplayKeepsActualLegacyNativeBindingUnlabelled(t *testing.T) {
	control, sender, owner, guest, _ := nativeReadyFixture(t)
	proof := control.native.(*receivedVMMProof)
	if err := newRunnerServer(sender).sendNativeReady(proof.session, "firecracker", owner, guest); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := control.waitReady(ctx); err != nil {
		t.Fatal(err)
	}
	spec := proof.spec
	connection := &ownedGuestLink{Conn: guest}
	// This fixture's real ordinary process/socket pair is the existing
	// legacy native seam. A copied mode string must not promote that issuer.
	s := &sandbox{driver: &Driver{kind: firecrackerBackend}, spec: spec, owner: t.Context(), uid: slotUIDModeV1Name,
		command: &fakeVMProcess{}, started: true, helloChecked: true, control: control, connection: connection, done: make(chan struct{})}
	binding := &NativeBindingV1{verifier: &sandboxNativeBinding{sandbox: s, connection: connection, proof: proof}}
	expected := vmneed.NeedV1{BootV1: spec.VM.Clone(), Driver: "firecracker", CPUs: spec.CPUs, DiskMax: spec.DiskMax}
	if own, err := OriginalAccountModeV1(t.Context(), s, connection, binding, expected); own || err != nil {
		t.Fatal("actual legacy native proof was promoted by a copied own-account mode:", err)
	}
}
