//go:build linux

package host

import (
	"context"
	"testing"
)

func TestNoneStopPublicationCannotOriginateFromCopiedCompletionData(t *testing.T) {
	for _, source := range []*StoppedNoneSessionV1{nil, {}} {
		if result, err := publishRequestedNoneStopV1(context.Background(), &SessionOperation{}, source); err == nil || result != nil {
			t.Fatal("copied N5 completion data supplied physical stop publication authority")
		}
	}
}
