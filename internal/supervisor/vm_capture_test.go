package supervisor

import (
	"errors"
	"testing"

	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/vmconfig"
)

func vmCaptureProofFixture() (*Supervisor, *Supervisor) {
	s, child := emptyProofFixture()
	nonce := "11111111111111111111111111111111"
	s.vmPlan = &vmconfig.Plan{Args: proto.CtlVMPrepareArgs{CaptureNonce: nonce, StateFiles: []string{".agent/history"}}}
	s.captureReady = nonce
	s.m.VMCaptureNonce = "22222222222222222222222222222222"
	return s, child
}

func TestVMCaptureRequiresAllowedKindAndEveryV2Member(t *testing.T) {
	for _, kind := range []string{host.EndStop, host.EndSystem, host.EndEmpty, host.EndSandbox} {
		s, child := vmCaptureProofFixture()
		if !s.vmCaptureEligible(kind, true, nil) {
			t.Fatalf("complete owned capture refused: %s", kind)
		}
		child.end = endFacts{}
		if s.vmCaptureEligible(kind, true, nil) {
			t.Fatalf("missing v2 member End promoted state: %s", kind)
		}
	}
	for _, kind := range []string{host.EndKill, host.EndBoot, "unknown"} {
		s, _ := vmCaptureProofFixture()
		previous := s.m.VMCaptureNonce
		if err := s.acceptVMCapture(kind, true, nil); err != nil || s.m.VMCaptureNonce != previous {
			t.Fatal("kill or unsupported end changed the prior accepted nonce", err)
		}
	}
}

func TestVMCaptureReadinessCannotReplaceCleanupOrOwnedCleanExit(t *testing.T) {
	s, _ := vmCaptureProofFixture()
	if s.vmCaptureEligible(host.EndStop, false, errors.New("cleanup failed")) {
		t.Fatal("failed cleanup promoted a nonce")
	}
	s.sb = &emptyTestRuntime{clean: false}
	if s.vmCaptureEligible(host.EndStop, false, nil) {
		t.Fatal("guest readiness manufactured a clean owned VMM exit")
	}
	s.sb = &emptyTestRuntime{clean: true}
	s.captureReady = "33333333333333333333333333333333"
	if s.vmCaptureEligible(host.EndStop, false, nil) {
		t.Fatal("a different generation's ready nonce was accepted")
	}
}
