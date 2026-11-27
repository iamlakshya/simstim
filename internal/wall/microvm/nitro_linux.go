//go:build linux

package microvm

import (
	"errors"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/nitrodev"
	"github.com/iamlakshya/simstim/internal/proto"
)

func (k backendKind) deviceAccess(facts api.CapProbe) (usable, restricted bool) {
	if k == nitroBackend {
		return facts.Nitro != nil && facts.Nitro.Usable, facts.Nitro != nil && facts.Nitro.Present && facts.Nitro.AccessRestricted
	}
	return facts.KVM.Usable, facts.KVM.Present && !facts.KVM.Usable && facts.KVM.AccessRestricted
}

func (s *sandbox) bootFailure(cause error) error {
	if s.driver.kind != nitroBackend {
		return cause
	}
	// Diagnostics come only from the owned native child and remain bounded.
	if strings.Contains(string(s.log.snapshot()), "Failed to query hypervisor-supported flags") {
		return errors.Join(&proto.Error{Code: "wall_unavailable", Message: "the parent Nitro driver lacks the sandbox interface", Hint: nitrodev.ParentKernelHint}, cause)
	}
	return cause
}

func (k backendKind) identityMode(facts api.CapProbe) string {
	if k == nitroBackend && !slotAccountModeV1(facts) {
		return "login"
	}
	return slotUIDModeV1(facts)
}
