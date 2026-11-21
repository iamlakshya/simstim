package supervisor

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/supply"
)

func init() { Features = append(Features, proto.FeatureManagerCustodyStopV1) }

func (s *Supervisor) managerCustodyStopRequestV1(client *client, request proto.Request) bool {
	if request.Op != proto.ManagerCustodyStopRoleV1 && request.Op != proto.ManagerCustodyStopCommitV1 {
		return false
	}
	root := s.owner()
	var args proto.NativeStopArgsV1
	if !client.full || !api.ExactJSONFields(request.Args, args) || json.Unmarshal(request.Args, &args) != nil || args.Version != 1 || args.Primary != root.id || !supply.IsHex64(args.Nonce) {
		client.reply(request.ID, nil, toProtoErr(execRefusal("the custody stop has no exact original outside scope")))
		return true
	}
	scope := client.nativeScopeV1()
	root.mu.Lock()
	main, owner := root.custody, root.bootOwner
	valid := scope != nil && scope.purpose == proto.NativeStopInspectV1 && scope.root == root && scope.owner == owner && scope.state.Nonce == args.Nonce && scope.state.Generation == root.generation && scope.cleaned == root.runtimeCleaned && root.runtimeCleaned != nil && !channelClosed(root.runtimeCleaned) && scope.runtime == root.sb && scope.lifetime == root.lifetimeContext() && main != nil && owner != nil && root.launched
	var state proto.NativeStopStateV1
	if valid {
		runtime, err := json.Marshal(root.m.RuntimeManifest)
		if root.m.RuntimeManifest == nil {
			runtime = nil
		}
		valid = err == nil && custodyStopDigestV1(root.m.VMNeedV1) == scope.state.Need && custodyStopDigestV1(runtime) == scope.state.Runtime
		state = scope.state
	}
	root.mu.Unlock()
	if !valid || !owner.Valid(root.id) || !owner.StartupCommittedV1() {
		client.reply(request.ID, nil, toProtoErr(execRefusal("the actual custody stop scope changed or ended")))
		return true
	}
	if request.Op == proto.ManagerCustodyStopRoleV1 {
		if request.ID < 20 || request.ID >= 25 {
			client.reply(request.ID, nil, toProtoErr(execRefusal("the custody stop selected an unknown kernel role")))
			return true
		}
		client.wmu.Lock()
		raw, err := json.Marshal(state)
		if err == nil {
			err = client.writeErr
		}
		if err == nil {
			_ = client.conn.SetWriteDeadline(time.Now().Add(clientWriteWait))
			err = main.WriteManagerStopRoleV1(scope.lifetime, owner, client.conn, proto.Response{ID: request.ID, OK: true, Result: raw}, request.ID-20)
			_ = client.conn.SetWriteDeadline(time.Time{})
		}
		if err != nil {
			client.writeErr = err
		}
		client.wmu.Unlock()
		if err != nil {
			client.close()
		}
		return true
	}
	if request.ID != 2 {
		client.reply(request.ID, nil, toProtoErr(execRefusal("the custody stop changed its original commit boundary")))
		return true
	}
	if err := main.AcceptManagerStopV1(scope.lifetime, owner); err != nil {
		client.reply(request.ID, nil, toProtoErr(err))
		return true
	}
	client.setNativeScopeV1(nil)
	root.requestTrustedStopV1()
	client.reply(request.ID, state, nil)
	root.signalTrustedStopV1()
	return true
}

func custodyStopDigestV1(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
