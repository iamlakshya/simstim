package microvm

import (
	"context"
	"fmt"
	"net"

	"github.com/iamlakshya/simstim/internal/vmneed"
)

// NativeBindingV1 is borrowed evidence from the engine's actual native VMM and
// host-side guest connection. Only a live engine-owned sandbox constructs it.
// It supplies no grant, original operation or start/stop authority by itself.
// Its zero value is invalid on every platform.
type NativeBindingV1 struct{ verifier nativeBindingVerifierV1 }

type nativeBindingVerifierV1 interface {
	checkNativeBinding(context.Context, net.Conn, vmneed.NeedV1) error
	sessionID() string
}

// SessionID is correlation data from this actual engine instance. Callers
// compare it with their protected operation before relying on Check.
func (b *NativeBindingV1) SessionID() string {
	if b == nil || b.verifier == nil {
		return ""
	}
	return b.verifier.sessionID()
}

// Check validates the exact Start-returned connection, complete original boot
// data and retained native lifetime. The issuer separately requires its actual
// owned OP, supervisor lease and fresh protected primary before using this fact.
func (b *NativeBindingV1) Check(ctx context.Context, connection net.Conn, expected vmneed.NeedV1) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if b == nil || b.verifier == nil || connection == nil {
		return fmt.Errorf("the VM has no engine-owned native binding")
	}
	return b.verifier.checkNativeBinding(ctx, connection, expected)
}

type ownedGuestLink struct{ net.Conn }
