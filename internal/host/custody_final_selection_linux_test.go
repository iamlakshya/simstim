//go:build linux

package host

import (
	"context"
	"encoding/json"
	"testing"
)

func TestFinalCustodySelectionDataCannotRebindAnOriginalBoot(t *testing.T) {
	raw := json.RawMessage(`{"need":"","runtime":""}`)
	for _, owner := range []*OwnedSupervisorLease{nil, {}, {native: &sessionFileLease{}}} {
		boot := &originalBootV1{owner: owner, loan: startupLoanV1{Purpose: "manager-custody"}}
		if _, err := owner.checkCustodyFinalSelectionV1(context.Background(), boot, &Manifest{}, raw); err == nil {
			t.Fatal("copied final selection minted an inherited original boot")
		}
		if boot.loan.Need != "" || boot.loan.Runtime != "" || len(boot.currentNeed) != 0 || boot.runtime != nil {
			t.Fatal("failed final selection changed the original boot source")
		}
	}
}
