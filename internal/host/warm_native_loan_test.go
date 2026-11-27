package host

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/iamlakshya/simstim/internal/proto"
)

func TestWarmOperationLoanCannotOriginateFromCopiedScope(t *testing.T) {
	raw, err := json.Marshal(proto.NativeStopArgsV1{Version: 1, Primary: "primary2", Nonce: "copied"})
	if err != nil {
		t.Fatal(err)
	}
	frameRaw, err := json.Marshal(proto.Request{ID: 8, Op: proto.NativeWarmLoanV1, Args: raw})
	if err != nil {
		t.Fatal(err)
	}
	for _, owner := range []*OwnedSupervisorLease{nil, {}} {
		for _, line := range []*StartupLineV1{nil, {}} {
			operation, err := owner.AcceptWarmOperationV1(context.Background(), line, proto.Frame{Type: proto.TRequest, Payload: frameRaw}, proto.NativeStopStateV1{Version: 1, Primary: "primary2", Nonce: "copied", Generation: "copied"})
			if err == nil || operation != nil {
				t.Fatal("copied native correlation supplied an incoming OP borrow")
			}
		}
	}
}
