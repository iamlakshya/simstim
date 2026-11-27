//go:build linux

package host

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/iamlakshya/simstim/internal/brand"
)

// checkNativeMemoryV1 is the last native check before the original OP loan or
// private start values leave the outside creator. Manager properties alone do
// not prove that the actual original process inhabits the exact capped unit.
func (launch *startupLaunchV1) checkNativeMemoryV1(ctx context.Context, m *Manifest) error {
	if !isWalledMemoryV1(m) {
		return nil
	}
	peer := launch.nativePeer()
	if peer == nil || peer.proc == nil {
		return memoryNativeFailureV1(errors.New("the original native supervisor process is unavailable"))
	}
	if err := peer.checkLive(); err != nil {
		return attachMemoryFactsV1(err, m.Limits.Declared)
	}
	data, err := peer.proc.ReadFile("cgroup")
	if err != nil {
		return memoryNativeFailureV1(err)
	}
	expected := fmt.Sprintf("0::/user.slice/user-%d.slice/user@%d.service/%s/%s\n", os.Getuid(), os.Getuid(), brand.Slice, UnitName(m.ID))
	// The canonical cgroup2 unit is exact. An ancestor, sibling, nested unit
	// or a path selected by metadata cannot prove this native membership.
	member := bytes.Equal(data, []byte(expected))
	native, err := readMemoryUnitV1(ctx, m.ID, true)
	if err != nil {
		return attachMemoryFactsV1(err, m.Limits.Declared)
	}
	if err := validateNativeMemoryBootV1(m, native, member); err != nil {
		return attachMemoryFactsV1(err, m.Limits.Declared)
	}
	return peer.checkLive()
}

func validateNativeMemoryBootV1(m *Manifest, native memoryUnitFactsV1, member bool) error {
	if m == nil || m.Limits.MemoryMax <= 0 || m.Limits.Declared != m.Limits.MemoryMax || !native.exists || native.maximum != m.Limits.MemoryMax || native.swap != 0 || !member {
		return UserError("no_room", "the actual supervisor has no exact native declared memory cap and zero-swap unit", "Use a working delegated systemd user manager and inspect the sandbox's native memory controller before starting it.")
	}
	return nil
}
