package egress

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestClassifierServiceHostEntersOnlyLocalInspectionBeforePathDecision(t *testing.T) {
	server := newMemberServer(t, InitArgs{Agent: "shell", Policy: api.Policy{Preset: api.PresetLocked, Unknown: api.UnknownClassify}})
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	before := snapshotDNSLedger(server.currentEngine())
	header := StreamHeader{Via: "explicit", Host: "discord.com", Port: 443}
	decision := server.awaitCarrierPolicy(ctx, nil, header, "CONNECT")
	if !decision.Allow || decision.Rule != "service:inspection" || !reflect.DeepEqual(before, snapshotDNSLedger(server.currentEngine())) {
		t.Fatal("fixed service host waited/called judge or acquired network authority before its path was visible")
	}
	if server.currentEngine().Decide(Request{Host: header.Host, Port: 443}).Allow {
		t.Fatal("local inspection probe became ordinary network permission")
	}
	deny := api.Policy{Preset: api.PresetLocked, Unknown: api.UnknownClassify, Rules: []api.Rule{{Layer: api.LayerSession, Target: header.Host}}}
	if err := server.replaceControlPolicy(memberJSON(t, PolicyUpdateArgs{Policy: deny})); err != nil {
		t.Fatal(err)
	}
	if server.classifierServiceInspection(server.currentEngine(), header.Host, 443) {
		t.Fatal("local inspection bypassed a matched deny")
	}
}
