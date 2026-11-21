//go:build linux

package host

import (
	"context"
	"testing"
)

func TestAutomaticNoneOriginRejectsCopiedGateAndSelectionData(t *testing.T) {
	for _, gate := range []*restoreGateV1{nil, {}} {
		if source, err := captureNoneAutomaticContinuationV1(context.Background(), &SessionOperation{}, gate); err == nil || source != nil {
			t.Fatal("copied restore metadata minted an actual automatic None continuation")
		}
	}
	origin := &noneAutomaticRestoreOriginV1{}
	origin.self = origin
	if err := origin.checkGateV1(context.Background()); err == nil {
		t.Fatal("an empty gate/OP became an automatic original owner")
	}
}
