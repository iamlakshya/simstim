package host

import (
	"fmt"
	"runtime"

	"github.com/iamlakshya/simstim/internal/vmneed"
)

// protectedNeedReferences is retention-only. It reads the SAME strict codec
// from the protected canonical manifest; layouts, inventory and guest claims
// never select pins. Future/invalid plans fail every collector closed.
func protectedNeedReferences(m Manifest) (vmneed.NeedV1, bool, error) {
	if len(m.Run.extra) != 0 {
		return vmneed.NeedV1{}, false, fmt.Errorf("trusted session %s has unknown runtime retention metadata", m.ID)
	}
	if len(m.VMNeedV1) == 0 {
		return vmneed.NeedV1{}, false, nil
	}
	need, err := vmneed.DecodeV1(m.VMNeedV1, runtime.GOARCH)
	if err != nil {
		return vmneed.NeedV1{}, false, fmt.Errorf("trusted session %s has unavailable VM retention authority: %w", m.ID, err)
	}
	if m.Agents != "" && m.Agents != need.Agents.SHA256 {
		return *need, false, fmt.Errorf("trusted session %s has conflicting agents retention references", m.ID)
	}
	return *need, true, nil
}
