package supervisor

import (
	"testing"

	"github.com/iamlakshya/simstim/internal/host"
)

func TestEndedPrimaryControllerCountsOnlyActuallyLaunchedChildren(t *testing.T) {
	root, child := emptyProofFixture()
	delete(root.launches, root.id)
	delete(root.expected, root.id)
	root.launched, root.end = false, endFacts{}
	if !root.correlatedEmpty() || !root.naturalEmpty(true, nil) {
		t.Fatal("an intentionally unlaunched ended primary blocked actual last-child completion")
	}
	child.end.claimed = false
	if root.naturalEmpty(true, nil) {
		t.Fatal("missing actual child End manufactured natural empty")
	}
	child.end.claimed = true
	root.rec.end = &host.LifeEnd{Kind: host.EndSystem}
	if root.captureEndEligible(host.EndKill, true, nil) {
		t.Fatal("controller-only mode weakened kill rollback")
	}
}
