package host

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/pullexport"
)

func TestOutputProgressScopeCannotDonatePointerIdentityAcrossReusedZeroRequest(t *testing.T) {
	rpc, err := NewPullRPC(t.Context(), func(context.Context, string, string) (pullexport.Source, error) {
		t.Fatal("scope check launched a source")
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer rpc.Close()
	owner := &bridgePull{PullRPC: rpc, routes: map[string]*pullRoute{}, aliases: map[string]*pullRoute{}, pending: map[uint64]*pullPending{}, relays: map[string]*LivePullRelay{}}
	route := &pullRoute{original: "fixture2", primary: "primary2", token: strings.Repeat("a", 26), epoch: 0}
	pending := &pullPending{route: route, read: true, token: route.token}
	owner.routes[route.primary] = route
	owner.aliases[route.token] = route
	owner.pending[0] = pending
	b := &bridge{pull: owner}
	scope := pullProgressScope{owner: owner, pending: pending, route: route, event: api.PullOutputProgress{Event: api.PullOutputProgressEvent, ID: route.original, Token: route.token, Epoch: 0, Request: 0, Next: 1}}
	if !b.pullProgressValid(scope) {
		t.Fatal("explicit zero IDs were treated as absent")
	}
	copy := *pending
	owner.pending[0] = &copy
	if b.pullProgressValid(scope) {
		t.Fatal("a reused numeric ID inherited the old read's progress")
	}
	owner.pending[0] = pending
	route.finishing = 1
	if b.pullProgressValid(scope) {
		t.Fatal("abort admission failed to stop progress immediately")
	}
	route.finishing = 0
	route.beginPending = true
	if b.pullProgressValid(scope) {
		t.Fatal("full preparation inherited the previous read's progress")
	}
	route.beginPending = false
	route.epoch = 1
	if b.pullProgressValid(scope) {
		t.Fatal("a new begin epoch inherited old progress")
	}
	route.epoch = 0
	data, err := json.Marshal(api.PullChunk{Next: 1, Data: []byte("x")})
	if err != nil {
		t.Fatal(err)
	}
	if b.bindPullProgress(proto.Response{ID: 0, OK: true, Result: data}) == nil {
		t.Fatal("actual current response failed to bind")
	}
	b.pullClosed = true
	if b.pullProgressValid(scope) || b.bindPullProgress(proto.Response{ID: 0, OK: true, Result: data}) != nil {
		t.Fatal("ended output scope was resurrected")
	}
}
