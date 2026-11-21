//go:build linux

package host

import (
	"context"
	"testing"
)

func TestAuxiliaryVacancyCannotOriginateFromMetadataOrMissingNativeOwner(t *testing.T) {
	for _, operation := range []*SessionOperation{nil, {}, {id: "primary2"}} {
		owner, err := captureAuxiliaryVacancyV1(context.Background(), operation)
		if err == nil || owner != nil {
			t.Fatal("metadata operation issued an auxiliary native vacancy")
		}
	}
	for _, owner := range []*auxiliaryVacancyV1{nil, {}, {operation: &SessionOperation{id: "primary2"}}} {
		if err := owner.Check(context.Background()); err == nil {
			t.Fatal("missing native manager/holder/OP was accepted")
		}
	}
}
