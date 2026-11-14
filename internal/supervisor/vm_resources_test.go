package supervisor

import (
	"context"
	"errors"
	"testing"

	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/wall/chv"
)

func TestVMReferencesCannotBeOverwrittenByOldSupervisorSnapshot(t *testing.T) {
	live := &host.Manifest{ID: "abcdef23", Wall: &host.WallRecord{Driver: "chv"}}
	live.Run.Image = "old-image"
	stale := *live
	live.Run.Image = "client-image"
	live.Run.Label = "client-runtime"
	live.Run.Artefacts = []string{"client-vmm", "client-kernel"}
	s := &Supervisor{m: live}
	s.mergeVMReferences(&stale)
	if stale.Run.Image != "client-image" || stale.Run.Label != "client-runtime" || len(stale.Run.Artefacts) != 2 {
		t.Fatal("an older snapshot clobbered current retention references")
	}
	stale.Run.Artefacts[0] = "mutated snapshot"
	if live.Run.Artefacts[0] != "client-vmm" {
		t.Fatal("a saved snapshot aliases the live reference vector")
	}
}

func TestVMResourceConsumerNeedsExpectedAuthorityAndUsesWholeBootBudget(t *testing.T) {
	s := &Supervisor{lifetime: context.Background(), id: "abcdef23", m: &host.Manifest{ID: "abcdef23", Wall: &host.WallRecord{Driver: "chv"}}}
	if s.bootLimit() != chv.BootTimeout {
		t.Fatal("the existing30s ordinary timeout cut the VM budget")
	}
	if _, err := s.sandboxForRun(); err == nil {
		t.Fatal("a host layout or manifest without authenticated Need supplied VM authority")
	}
	if err := RunWithExpectedSpec(context.Background(), s.id); err == nil {
		t.Fatal("VM entrypoint accepted missing expected authority")
	}
	if got := vmRuntimeError("sandbox_boot", context.Canceled); !errors.Is(got, context.Canceled) {
		t.Fatal("boot cancellation lost its identity")
	}
	var wire *proto.Error
	if !errors.As(vmRuntimeError("sandbox_error", errors.New("external SIGKILL")), &wire) || wire.Code != "sandbox_error" {
		t.Fatal("an external kill was inferred to be OOM")
	}
}
