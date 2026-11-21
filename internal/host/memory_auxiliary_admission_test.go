//go:build linux

package host

import (
	"context"
	"testing"
	"time"
)

func TestAuxiliaryDataCannotMintRealAccountingOrigin(t *testing.T) {
	m := auxiliaryMemoryFixtureV1(time.Now().UTC())
	if _, err := admitAuxiliaryMemoryLockedV1(context.Background(), nil, m.Auxiliary); err == nil {
		t.Fatal("reservation data minted an original auxiliary owner")
	}
	if err := checkAuxiliaryMemoryStartLockedV1(context.Background(), nil, m.Auxiliary); err == nil {
		t.Fatal("reservation data granted native dispatch")
	}
}
