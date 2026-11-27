package client

import "context"

func sessionMachineOwnerOfV1(ctx context.Context) *SessionMachineOwnerV1 {
	owner, _ := ctx.Value(sessionMachineOwnerKeyV1{}).(*SessionMachineOwnerV1)
	return owner
}

func sessionMachineInitialRuntimeV1(ctx context.Context, host Host, hostID string, request *GrantRequest) bool {
	owner := sessionMachineOwnerOfV1(ctx)
	if owner == nil || owner.closed.Load() || owner.committed.Load() || owner.resource == nil || owner.capture.target == nil || request == nil {
		return false
	}
	request.mu.Lock()
	intent := request.runtimeUpgrade
	request.mu.Unlock()
	if intent == nil || intent.conn == nil || intent.hostID != hostID || intent.conn.route.machineOwner != owner || !SameEntry(intent.conn.Host, host) {
		return false
	}
	return owner.capture.target.checkConn(ctx, intent.conn) == nil
}

func allowSandboxPrepareFloorV1(target *HostPrepareTargetV1, level string) error {
	owner := target.machineOwner
	if owner != nil && !owner.closed.Load() && !owner.committed.Load() && owner.downgrade && owner.capture.target == target && level == "process" && IsSessionMachineV1(target.host) {
		return nil
	}
	return allowFloor(target.host, target.tier, Outbound{Kind: OutboundFloor, Name: level})
}
