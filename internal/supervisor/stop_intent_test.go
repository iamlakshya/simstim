package supervisor

import (
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/host"
)

func TestGracefulSystemEndKeepsEveryPreviouslyActiveMemberRestorable(t *testing.T) {
	root, child := emptyProofFixture()
	root.end, child.end = endFacts{}, endFacts{}
	root.m.Wall, child.m.Wall = &host.WallRecord{Level: "microvm", Driver: "chv"}, &host.WallRecord{Level: "microvm", Driver: "chv"}
	root.markStopIntent()
	for _, member := range []*Supervisor{root, child} {
		member.claimEnded(claim{runToken: member.runToken, exitCode: 143})
		if !member.end.claimed || !member.end.afterStop || member.end.token != member.runToken {
			t.Fatal("intentional stop discarded actual physical End correlation")
		}
		member.rec.end = &host.LifeEnd{Kind: host.EndSystem, At: time.Now()}
		status := member.endStatus()
		if status.State != host.StateWorking || status.Source != "system" {
			t.Fatalf("system stop became permanent natural exit for %s: %+v", member.id, status)
		}
		member.closeRecord(host.EndSystem, "stopped with the host")
		saved := member.snapshot()
		if saved.Ended() || saved.Life.End.Claimed {
			t.Fatal("physical stop correlation became permanent protected natural-end intent")
		}
	}
	if !root.correlatedEmpty() {
		t.Fatal("system stop lost VMv2 all-member capture evidence")
	}
}

func TestStopIntentPreservesPreviouslyObservedIndividualEnd(t *testing.T) {
	root, _ := emptyProofFixture()
	root.markStopIntent()
	if root.end.afterStop {
		t.Fatal("stop intent rewrote an earlier natural end")
	}
	root.rec.end = &host.LifeEnd{Kind: host.EndSystem, At: time.Now()}
	if status := root.endStatus(); status.State != host.StateErrored || status.ExitCode == nil || *status.ExitCode != 1 {
		t.Fatalf("the earlier nonzero agent exit was lost: %+v", status)
	}
}
