package client

import (
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/proto"
)

// Follow the production creator-to-registration handoff with an actual
// protocol Hello. The bridge stays in memory; no provider or host runs here.
func registerSessionMachineFixtureV1(t *testing.T, owner *SessionMachineOwnerV1) {
	t.Helper()
	ctx := owner.RegistrationContext()
	if ctx == nil {
		t.Fatal("creation did not retain its original registration context")
	}
	creator := ctx.Value(hostStateObservationKey{}).(*HostStateObservation)
	if creator.role != hostStateRead || creator.op == nil || creator.op.gone.Load() {
		t.Fatal("creation did not hand off its original Source and physical OP")
	}
	stageCtx, abort, err := StagePinOwned(ctx, owner.Host().Name)
	if err != nil {
		t.Fatal(err)
	}
	defer abort()
	host := owner.Host()
	change := HostChange{Kind: "host add", Was: &host, Now: &host, Pin: true}
	observation, err := ObserveHostRegistrationContext(stageCtx, change)
	if err != nil {
		t.Fatal(err)
	}
	registration := WithHostStateObservation(stageCtx, observation)
	bridge := newPrepareBridgeTransport(t, strings.Repeat("8", 32), []string{"session.id"}, func(request proto.Request, _ *proto.Writer) (any, *proto.Error, bool) {
		return nil, &proto.Error{Code: "unknown", Message: request.Op}, false
	})
	conn, err := dial(registration, host, route{t: bridge, machineOwner: owner})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := observation.checkOwner(registration); err != nil {
		t.Fatal(err)
	}
	change.HostID = conn.HostID()
	if change.HostID != strings.Repeat("8", 32) {
		t.Fatal("registration did not use the accepted helper identity")
	}
	if err := UpdateHosts(registration, change); err != nil {
		t.Fatal(err)
	}
	if owner.Target().HostID() != conn.HostID() {
		t.Fatal("the actual registration writer lost its checked identity")
	}
	if err := owner.joinConnectionsV1(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := owner.FinishRegistration(); err != nil {
		t.Fatal(err)
	}
	if err := owner.Target().Check(t.Context()); err != nil {
		t.Fatal(err)
	}
}
