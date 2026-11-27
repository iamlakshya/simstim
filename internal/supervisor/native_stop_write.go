package supervisor

import (
	"encoding/json"
	"time"

	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
)

func (client *client) replyNativeStopProofV1(owner *host.OwnedSupervisorLease, id uint64, state proto.NativeStopStateV1) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	client.wmu.Lock()
	defer client.wmu.Unlock()
	if client.writeErr != nil {
		return client.writeErr
	}
	_ = client.conn.SetWriteDeadline(time.Now().Add(clientWriteWait))
	if scope := client.nativeScopeV1(); scope != nil && scope.purpose == proto.NativeResumeInspectV1 && scope.none != nil && host.IsNoneSupervisor(scope.root.m) {
		err = owner.WriteNativeNoneWarmProofV1(scope.lifetime, client.conn, proto.Response{ID: id, OK: true, Result: raw}, scope.none)
	} else {
		err = owner.WriteNativeStopProofV1(owner.Lifetime(), client.conn, proto.Response{ID: id, OK: true, Result: raw})
	}
	_ = client.conn.SetWriteDeadline(time.Time{})
	if err != nil {
		client.writeErr = err
	}
	return err
}
