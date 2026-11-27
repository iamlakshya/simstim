package supervisor

import (
	"errors"
	"testing"

	"github.com/iamlakshya/simstim/internal/host"
)

func TestCaptureKindCannotPromoteKilledOrBrokenVM(t *testing.T) {
	for _, kind := range []string{host.EndKill, host.EndBoot, "unknown"} {
		s, _ := emptyProofFixture()
		if s.captureEndEligible(kind, true, nil) {
			t.Fatalf("clean poweroff promoted forbidden termination %s", kind)
		}
	}
	for _, kind := range []string{host.EndStop, host.EndSystem} {
		s, child := emptyProofFixture()
		// Explicit stop/system remains eligible without a natural event from
		// a legacy holder, provided the trusted owned runtime exited cleanly.
		s.end, child.end = endFacts{}, endFacts{}
		if !s.captureEndEligible(kind, false, nil) {
			t.Fatalf("clean explicit termination lost retained state: %s", kind)
		}
		for _, change := range []func(){
			func() { s.lost = "link failure" },
			func() { s.silent = true },
			func() { s.rec.oom = true },
			func() { s.captureFailed = true },
		} {
			s.lost, s.silent, s.rec.oom, s.captureFailed = "", false, false, false
			change()
			if s.captureEndEligible(kind, false, nil) {
				t.Fatalf("incomplete explicit cleanup promoted state: %s", kind)
			}
		}
	}
}

func TestCurrentNaturalCaptureNeedsEveryMemberAndCleanup(t *testing.T) {
	s, child := emptyProofFixture()
	if !s.captureEndEligible(host.EndEmpty, true, nil) {
		t.Fatal("complete current natural exit refused")
	}
	if s.captureEndEligible(host.EndEmpty, false, nil) || s.captureEndEligible(host.EndEmpty, true, errors.New("cleanup")) {
		t.Fatal("readiness manufactured owned exit or cleanup")
	}
	child.end = endFacts{}
	if s.captureEndEligible(host.EndSandbox, true, nil) {
		t.Fatal("missing current member end promoted a natural capture")
	}
}
