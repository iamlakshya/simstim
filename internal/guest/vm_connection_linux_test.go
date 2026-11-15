//go:build linux

package guest

import (
	"context"
	"net"
	"testing"

	"github.com/iamlakshya/simstim/internal/proto"
)

func TestOrdinaryConnectionCannotMintVerifiedVMInitOwner(t *testing.T) {
	local, peer := net.Pipe()
	t.Cleanup(func() { _ = local.Close(); _ = peer.Close() })
	if owner, err := newVMInitConnection(t.Context(), &Init{}, local); err == nil || owner != nil {
		t.Fatal("an ordinary connection became native VM init provenance")
	}
}

func TestVMPrepareWithoutOriginalConnectionRefusesBeforeFilesystemEffects(t *testing.T) {
	vm := &vmRuntime{}
	// No filesystem/mount provider is installed. A valid ordinary DTO cannot
	// construct connection provenance or reach any filesystem effect.
	args := proto.CtlVMPrepareArgs{Session: "abcd2345", VM: proto.VMStartV1{Version: 1, System: "reset", StateDirs: []string{}}, StateFiles: []string{}}
	if _, err := vm.prepare(args); err == nil || vm.applied || vm.preparing || vm.failed {
		t.Fatal("a control DTO entered preparation without its original connection")
	}
}

func TestVMConnectionOwnerCannotMoveToAnotherInit(t *testing.T) {
	first := &Init{lifetime: context.Background()}
	second := &Init{lifetime: context.Background()}
	owner := &vmInitConnection{owner: t.Context(), init: first}
	if err := owner.check(second); err == nil {
		t.Fatal("a different Init adopted the original connection owner")
	}
	if err := (*vmInitConnection)(nil).check(first); err == nil {
		t.Fatal("an absent connection owner became preparation provenance")
	}
}
