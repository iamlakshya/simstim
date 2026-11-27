package host

import (
	"encoding/json"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

type pullProgressScope struct {
	owner   *bridgePull
	pending *pullPending
	route   *pullRoute
	event   api.PullOutputProgress
}

// bindPullProgress runs before writing an already formed successful read
// response. Its returned callback is used only by actual owned raw-write ACKs.
func (b *bridge) bindPullProgress(response proto.Response) func() {
	if !response.OK {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	owner := b.pull
	if owner == nil || b.pullClosed {
		return nil
	}
	pending := owner.pending[response.ID]
	if pending == nil || !pending.read {
		return nil
	}
	var chunk struct {
		Next int64 `json:"next"`
	}
	if json.Unmarshal(response.Result, &chunk) != nil || chunk.Next < pending.offset || chunk.Next-pending.offset > api.PullReadMax {
		return nil
	}
	route := pending.route
	scope := pullProgressScope{owner: owner, pending: pending, route: route, event: api.PullOutputProgress{Event: api.PullOutputProgressEvent, ID: route.original, Token: pending.token, Epoch: route.epoch, Request: response.ID, Next: chunk.Next}}
	if !b.pullProgressValidLocked(scope) {
		return nil
	}
	return func() { b.reportPullProgress(scope) }
}

func (b *bridge) pullProgressValidLocked(scope pullProgressScope) bool {
	return !b.pullClosed && b.pull == scope.owner && scope.owner.Context().Err() == nil && scope.owner.routes[scope.route.primary] == scope.route && scope.owner.pending[scope.event.Request] == scope.pending && scope.route.original == scope.event.ID && scope.route.token == scope.event.Token && scope.route.epoch == scope.event.Epoch && !scope.route.beginPending && scope.route.finishing == 0
}

func (b *bridge) pullProgressValid(scope pullProgressScope) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.pullProgressValidLocked(scope)
}

func (b *bridge) reportPullProgress(scope pullProgressScope) {
	b.mu.Lock()
	if !b.pullProgressValidLocked(scope) {
		b.mu.Unlock()
		return
	}
	if !scope.route.live {
		// Holding routing ownership prevents an old callback racing a reused
		// nonce/request into a different local manager export.
		scope.owner.Touch(scope.event)
		b.mu.Unlock()
		return
	}
	relay := scope.owner.relays[scope.route.primary]
	b.mu.Unlock()
	if relay != nil {
		// This enqueues only one latest scalar event. The canonical worker
		// revalidates under its send turn and never blocks output delivery.
		relay.ReportProgress(scope.event, func() bool { return b.pullProgressValid(scope) })
	}
}
