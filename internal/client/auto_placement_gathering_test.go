package client

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/iamlakshya/simstim/internal/proto"
)

func autoGatherGateFixtureV1(t *testing.T) (context.Context, *GrantRequest, *autoPlacementGatheringV1, *Conn) {
	t.Helper()
	candidate := placementCandidateFixture(t, nil, nil)
	conn := finishGeneratedTransportFixture(t, candidate.host, candidate.observation.hostID, "facts", "sandbox")
	request := NewGrantRequest(false, false)
	ctx := WithGrantRequest(t.Context(), request)
	if err := requireGrantOperation(ctx, grantOpNew); err != nil {
		t.Fatal(err)
	}
	selection := &HostSelectionV1{}
	plan := &AutoPlacementPlanV1{selection: selection}
	gathering := &autoPlacementGatheringV1{request: request, plan: plan, candidate: candidate, selection: selection,
		host: candidate.host, hostID: candidate.observation.hostID, tier: candidate.tier, source: candidate.observation.source, wakeClass: 2}
	gathering.wakeScope = grantScope{host: gathering.host.Name, hostID: gathering.hostID, tier: gathering.tier,
		kind: OutboundKind("placement-wake-v1"), name: gathering.source.generation + "/paid wake", purpose: grantRemote, operation: grantOpNew}
	request.autoGather = gathering
	return ctx, request, gathering, conn
}

func TestAutoGatherNegativeGateAllowsOnlyExactMetadataEvenDuringLocalBind(t *testing.T) {
	ctx, request, gathering, conn := autoGatherGateFixtureV1(t)
	if err := checkAutoNewReadyV1(ctx, request); err == nil {
		t.Fatal("gathering opened value selection")
	}
	transition := context.WithValue(ctx, autoPlacementBindingKeyV1{}, gathering)
	if err := checkAutoNewReadyV1(transition, request); err != nil {
		t.Fatal("same-object strict local binding scope was refused", err)
	}
	for _, tc := range []struct {
		operation, body string
		args            any
		allowed         bool
	}{
		{"host.facts", "{}", struct{}{}, true},
		{"host.facts", `{"extra":"payload"}`, map[string]string{"extra": "payload"}, false},
		{"ls", "null", nil, true},
		{"sandbox.probe", "{}", struct{}{}, true},
		{"sandbox.probe", `{"release_url":"https://remote.example/"}`, struct{}{}, false},
		{"new", "{}", NewArgs{}, false},
		{"runtime.status", "{}", struct{}{}, false},
		{"input.prepare", "{}", struct{}{}, false},
		{"policy.replace", "{}", struct{}{}, false},
	} {
		for _, scope := range []context.Context{ctx, transition} {
			err := checkAutoNewWireV1(scope, conn, proto.Request{ID: 1, Op: tc.operation, Args: json.RawMessage(tc.body)}, tc.args)
			if (err == nil) != tc.allowed {
				t.Fatalf("wire scope operation=%s allowed=%v error=%v", tc.operation, tc.allowed, err)
			}
		}
	}
	if err := checkAutoNewReadyV1(context.WithValue(ctx, autoPlacementBindingKeyV1{}, &autoPlacementGatheringV1{}), request); err == nil {
		t.Fatal("another private object opened the original gathering gate")
	}
}

func TestAutoWakeConfirmationExceptionAcceptsOnlyExactSameRequestScope(t *testing.T) {
	ctx, request, gathering, _ := autoGatherGateFixtureV1(t)
	question := &scopedGrantConfirmation{ConfirmError: &ConfirmError{}, request: request, scope: gathering.wakeScope}
	if _, err := ConfirmGrant(ctx, question); err != nil {
		t.Fatal(err)
	}
	request.mu.Lock()
	if !freshAutoPlacementWakeAnswersV1(request) {
		t.Fatal("the exact paid wake could not precede strict bind")
	}
	foreign := gathering.wakeScope
	foreign.hostID = "ffffffffffffffffffffffffffffffff"
	request.confirmed[foreign] = true
	if freshAutoPlacementWakeAnswersV1(request) {
		t.Fatal("another host identity donated its confirmation")
	}
	delete(request.confirmed, foreign)
	gathering.ready = true
	if freshAutoPlacementWakeAnswersV1(request) {
		t.Fatal("an old ready object's answer became a fresh request exception")
	}
	request.mu.Unlock()
	if grantRequestOf(request.requestContext) != request || request.requestContext.Value(autoPlacementBindingKeyV1{}) != nil {
		t.Fatal("a local binding transition escaped into the retained request context")
	}
}

func TestAutoStoppedCacheCanRankButCannotSatisfyStrictNewBinder(t *testing.T) {
	store, live := placementStoreFixture(t)
	if err := store.rememberFacts(t.Context(), []*hostFactsObservation{live.observation}, []Host{live.host}); err != nil {
		t.Fatal(err)
	}
	cached, err := store.cachedFacts(t.Context(), live.host)
	if err != nil {
		t.Fatal(err)
	}
	stopped := *live
	stopped.observation, stopped.cached, stopped.stopped, stopped.wakeClass = nil, cached, true, 1
	if reason, err := filterCachedPlacementFactsV1(t.Context(), &stopped, 0, true, true); err != nil || reason != "" {
		t.Fatal("checked stopped cache failed selection metadata filter", reason, err)
	}
	rankPlacementCandidates([]*autoPlacementCandidate{&stopped}, PlacementSettings{Strategy: "spread"}, nil)
	selection := &HostSelectionV1{}
	if _, err := selection.bindSelectedAutoNewRequestV1(t.Context(), NewGrantRequest(false, false), &stopped); err == nil {
		t.Fatal("cached data was reconstructed as accepted facts or native binding authority")
	}
	if err := SaveHosts(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if err := SaveHosts(t.Context(), []Host{live.host}); err != nil {
		t.Fatal(err)
	}
	if _, err := filterCachedPlacementFactsV1(t.Context(), &stopped, 0, true, true); err == nil {
		t.Fatal("cached ranking renewed a removed and re-added Source")
	}
}

func TestAutoReservationRefusalOwnsOnlyCanonicalPrewriteNegativeWitness(t *testing.T) {
	store, candidate := placementStoreFixture(t)
	first, err := store.reservePlacement(t.Context(), candidate, "awake, most room")
	if err != nil {
		t.Fatal(err)
	}
	defer first.finish(t.Context(), "", true)
	second, err := store.reservePlacement(t.Context(), candidate, "awake, most room")
	var refusal *placementReservationRefusalV1
	if second != nil || !errors.As(err, &refusal) || refusal.store != store || refusal.candidate != candidate {
		t.Fatal("prewrite locked room refusal lost its exact negative witness", second, err)
	}
	if _, ok := error(&UserError{Code: "no_room"}).(*placementReservationRefusalV1); ok {
		t.Fatal("a public no_room code created the private witness")
	}
}
