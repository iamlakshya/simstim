//go:build linux

package host

import (
	"context"
	"testing"
)

func TestExplicitStopPublicationCannotOriginateFromDataOnlySource(t *testing.T) {
	for _, source := range []*StoppedSessionV1{nil, {}} {
		if manifest, err := publishRequestedStopV1(context.Background(), &SessionOperation{}, source); err == nil || manifest != nil {
			t.Fatal("a copied or absent stopped value supplied physical publication authority")
		}
	}
}
