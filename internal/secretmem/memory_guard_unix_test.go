//go:build unix

package secretmem_test

import (
	"testing"

	"github.com/iamlakshya/simstim/internal/secretmem"
	"github.com/iamlakshya/simstim/internal/secretmem/probetest"
)

func memoryGuardChild() bool {
	if probetest.Mode() != "canonical-guard" {
		return false
	}
	child := probetest.BeginChild("canonical")
	if secretmem.Protect() != nil {
		child.Fail("canonical_protect_failed")
	}
	child.ObserveAndExit("canonical.after_protect")
	return true
}

func TestProtectInActualOwnedChild(t *testing.T) {
	report := probetest.Run(t, "canonical-guard")
	probetest.RequireProtected(t, report, "canonical.after_protect")
}
