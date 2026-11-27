//go:build linux

package microvm

import (
	"context"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/supply"
)

func TestNitroNativeReadyBindsDriverToOriginalKernelHandles(t *testing.T) {
	for _, senderDriver := range []string{"nitro", "firecracker"} {
		t.Run(senderDriver, func(t *testing.T) {
			control, sender, owner, guest, _ := nativeReadyFixture(t)
			proof := control.native.(*receivedVMMProof)
			// This ordinary child supplies genuine pidfd/socket evidence for the
			// private protocol seam. It does not emulate or prove an EC2 VM boot.
			proof.spec.Driver = "nitro"
			for i := range proof.spec.VM.Runtime.Items {
				item := &proof.spec.VM.Runtime.Items[i]
				if item.Kind == supply.KindKernel {
					item.Name = "guest-efi"
				}
			}
			if err := newRunnerServer(sender).sendNativeReady(proof.session, senderDriver, owner, guest); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			err := control.waitReady(ctx)
			if senderDriver == "nitro" {
				if err != nil {
					t.Fatal("original native Nitro message refused", err)
				}
				if err := proof.check(); err != nil {
					t.Fatal("retained native source lost", err)
				}
			} else if err == nil || proof.check() == nil {
				t.Fatal("the KVM driver's native message qualified a Nitro plan")
			}
		})
	}
}
