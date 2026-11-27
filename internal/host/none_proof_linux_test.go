//go:build linux

package host

import (
	"context"
	"testing"

	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/wall/none"
)

func TestNoneWarmProofRejectsCopiedContextAndUnreadyOwner(t *testing.T) {
	owner := &OwnedSupervisorLease{}
	birth := &none.LiveOwnerV1{}
	if err := owner.WriteNativeNoneWarmProofV1(context.Background(), nil, proto.Response{}, birth); err == nil {
		t.Fatal("an unready owner issued a none warm proof")
	}
	copy := &OriginalVMNeed{}
	ctx := context.WithValue(context.Background(), originalBootContextKeyV1{}, copy)
	if err := owner.WriteNativeNoneWarmProofV1(ctx, nil, proto.Response{}, birth); err == nil {
		t.Fatal("a copied context issued a none warm proof")
	}
	if err := copy.BindOriginalNoneBirthV1(ctx, birth, nil); err == nil {
		t.Fatal("a copied startup role or nil connection bound a new native creator")
	}
	var absent *StoppedNoneSessionV1
	if err := absent.Check(context.Background(), &SessionOperation{}); err == nil {
		t.Fatal("a stopped none DTO issued an N5 source")
	}
}
