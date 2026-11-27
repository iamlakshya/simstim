package jail

import (
	"os"
	"os/exec"
	"testing"
)

func preparedGvisorDescriptionsV1(t *testing.T) *gvisorOuterOwnerV1 {
	t.Helper()
	left, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	right, err := os.Open("/dev/null")
	if err != nil {
		left.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = left.Close(); _ = right.Close() })
	return &gvisorOuterOwnerV1{launcher: left, helper: right}
}

// No namespace/program is created: this tests only actual retained resource
// descriptions and the atomic Start/preparation-release fence.
func TestGvisorUnstartedPreparedReleaseKeepsCloseError(t *testing.T) {
	owner := preparedGvisorDescriptionsV1(t)
	command := &Cmd{cmd: &exec.Cmd{}, gvisorOuter: owner}
	if err := owner.helper.Close(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := command.ReleasePreparedV1(); err == nil {
			t.Fatal("native code description error became a successful cleanup retry")
		}
	}
	if !command.physical || command.phase != phaseFailed {
		t.Fatal("known unstarted owner was not fenced")
	}
	if err := command.Start(); err == nil {
		t.Fatal("retired actual preparation started again")
	}
}

func TestGvisorPreparedReleaseRefusesUnknownNativeLifetime(t *testing.T) {
	owner := preparedGvisorDescriptionsV1(t)
	command := &Cmd{cmd: &exec.Cmd{}, gvisorOuter: owner, owned: true, phase: phaseRunning}
	if err := command.ReleasePreparedV1(); err == nil {
		t.Fatal("unknown original native lifetime released its code")
	}
	if _, err := owner.helper.Stat(); err != nil {
		t.Fatal("refusal discarded the actual retained preparation", err)
	}
}
