//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/iamlakshya/simstim/internal/api"
)

type custodyFinalSelectionV1 struct {
	Need    string `json:"need"`
	Runtime string `json:"runtime"`
}

// Only the actual already authenticated startup line reaches this check.
// Digests correlate its final SAME-OP publication; they issue no selection.
func (owner *OwnedSupervisorLease) checkCustodyFinalSelectionV1(ctx context.Context, boot *originalBootV1, m *Manifest, raw json.RawMessage) (custodyFinalSelectionV1, error) {
	var selection custodyFinalSelectionV1
	if owner == nil || owner.native == nil || owner.custody == nil || ctx == nil {
		return selection, sessionLeaseError("the final custody selection has no actual inherited main")
	}
	if !api.ExactJSONFields(raw, selection) || json.Unmarshal(raw, &selection) != nil || boot == nil || boot.owner != owner || boot.loan.Purpose != "manager-custody" || m == nil || m.ID != owner.native.id || m.Status.State != StateStarting || m.Run.BootID != BootID() {
		return selection, sessionLeaseError("the final custody request has no exact original restart selection")
	}
	runtime, err := startupRuntimeBytes(m)
	if err != nil || startupDigest(m.VMNeedV1) != selection.Need || startupDigest(runtime) != selection.Runtime {
		return selection, errors.Join(sessionLeaseError("the final custody request differs from its canonical same-operation selection"), err)
	}
	if err := ValidateManifestStart(m); err != nil {
		return selection, err
	}
	for _, item := range []struct {
		name string
		want uint64
	}{{"MemoryMax", uint64(m.Limits.MemoryMax)}, {"MemorySwapMax", 0}} {
		value, err := owner.custody.manager.getProperty(ctx, owner.custody.path, systemdServiceInterface, item.name)
		got, ok := value.Value().(uint64)
		if err != nil || !ok || got != item.want {
			return selection, errors.Join(sessionLeaseError("the final custody main has another actual native cap"), err)
		}
	}
	return selection, ctx.Err()
}
