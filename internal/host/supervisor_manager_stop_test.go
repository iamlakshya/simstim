package host

import (
	"context"
	"testing"
)

func TestOptionalManagerStopOwnerCannotOriginateFromMissingNativeProducer(t *testing.T) {
	for _, owner := range []*OwnedSupervisorLease{nil, {}} {
		if err := owner.RetainManagerStopForSameRequestV1(context.Background()); err == nil {
			t.Fatal("missing manager owner was accepted as transient or custody authority")
		}
	}
}
