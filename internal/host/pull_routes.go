package host

import (
	"context"
	"encoding/json"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// Routes account only for outer RPC aliases. Actual sources, processes,
// primary leases and spools belong to the one canonical backend manager.
type pullRoute struct {
	original, primary, token string
	beginToken               string
	repo                     string
	beginID                  uint64
	beginPending             bool
	epoch                    uint64
	live                     bool
	finishing                int
}

type pullPending struct {
	route           *pullRoute
	begin, initial  bool
	read            bool
	offset          int64
	previousEpoch   uint64
	token, previous string
}

func pullRouteError(message string) error {
	return UserError("pull_protocol", message, "Close the current pull and retry with its complete original address.")
}

func (b *bridge) prepareRoutedPull(owner *bridgePull, req proto.Request) (func(context.Context) (any, error), context.Context, error) {
	var route *pullRoute
	var err error
	if req.Op == api.OpPullBegin {
		route, err = b.admitPullRoute(owner, req)
	} else {
		route, err = b.existingPullRoute(owner, req)
	}
	if err != nil {
		return nil, nil, err
	}
	if route == nil {
		return func(context.Context) (any, error) { return api.PulledResult{}, nil }, owner.Context(), nil
	}
	if route.live {
		relay, err := b.livePullRelay(owner, route.primary)
		if err == nil {
			err = relay.Forward(req)
		}
		if err != nil {
			b.observePullError(req.ID, err)
			return nil, nil, err
		}
		return nil, owner.Context(), nil
	}
	run, err := owner.Prepare(req)
	if err != nil {
		return nil, nil, err
	}
	return run, owner.Context(), nil
}

func (b *bridge) admitPullRoute(owner *bridgePull, req proto.Request) (*pullRoute, error) {
	var args api.PullBeginArgs
	if !api.ExactJSONFields(req.Args, args) || json.Unmarshal(req.Args, &args) != nil || args.Repo != "" && !api.InputRepoName(args.Repo) || !api.ValidSessionID(args.ID) || !api.ValidSessionID(args.Token) || len(args.Token) != 26 {
		return nil, pullRouteError("the pull admission identity is invalid")
	}
	primary, err := SandboxPrimaryID(args.ID)
	if err != nil {
		return nil, err
	}
	m, err := LoadManifest(primary)
	if err != nil {
		return nil, err
	}
	live := m.Wall != nil && SessionRunning(m)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.pullClosed {
		return nil, pullRouteError("the pull connection has ended")
	}
	if _, used := owner.pending[req.ID]; used || owner.aliases[args.Token] != nil {
		return nil, pullRouteError("the pull request identity is already in use")
	}
	route := owner.routes[primary]
	initial := route == nil
	if initial {
		if args.Previous != "" || len(owner.routes) >= api.PullExportsMax {
			return nil, UserError("sandbox_busy", "the prior export is unavailable or all pull slots are in use", "Finish or cancel the current pulls before starting another.")
		}
		route = &pullRoute{original: args.ID, primary: primary, token: args.Token, repo: args.Repo, live: live}
		owner.routes[primary] = route
	} else if route.original != args.ID || route.repo != args.Repo || !args.Full || args.Previous != route.token || route.beginPending || route.finishing != 0 {
		return nil, UserError("sandbox_busy", "this primary already has a different owned pull", "Finish or cancel that pull before starting another.")
	}
	if !initial {
		for _, pending := range owner.pending {
			if pending.route.primary == primary && pending.read {
				return nil, UserError("sandbox_busy", "the previous pull read is still being delivered", "Wait for that read response before retrying the history.")
			}
		}
	}
	previousEpoch := route.epoch
	route.beginID, route.beginToken, route.epoch = req.ID, args.Token, req.ID
	route.beginPending = true
	owner.aliases[args.Token] = route
	owner.pending[req.ID] = &pullPending{route: route, begin: true, initial: initial, token: args.Token, previous: args.Previous, previousEpoch: previousEpoch}
	return route, nil
}

func (b *bridge) existingPullRoute(owner *bridgePull, req proto.Request) (*pullRoute, error) {
	var args api.PulledArgs
	if json.Unmarshal(req.Args, &args) != nil || !api.ValidSessionID(args.ID) || !api.ValidSessionID(args.Token) || len(args.Token) != 26 {
		return nil, pullRouteError("the pull continuation identity is invalid")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.pullClosed {
		return nil, pullRouteError("the pull connection has ended")
	}
	route := owner.aliases[args.Token]
	if route == nil {
		if req.Op == api.OpPulled && len(args.Commits) == 0 {
			return nil, nil
		}
		return nil, pullRouteError("this connection does not own that pull nonce")
	}
	if route.original != args.ID {
		return nil, pullRouteError("the pull continuation changed its original address")
	}
	if _, used := owner.pending[req.ID]; used {
		return nil, pullRouteError("the pull request identity is already pending")
	}
	if req.Op == api.OpPullRead {
		var read api.PullReadArgs
		if json.Unmarshal(req.Args, &read) != nil || read.Offset < 0 {
			return nil, pullRouteError("the read offset is invalid")
		}
		for _, pending := range owner.pending {
			if pending.route.primary == route.primary && pending.read {
				return nil, UserError("sandbox_busy", "this pull already has an in-flight read", "Wait for its exact offset response before reading again.")
			}
		}
		owner.pending[req.ID] = &pullPending{route: route, token: args.Token, read: true, offset: read.Offset}
	}
	if req.Op == api.OpPulled {
		if !args.Abort {
			for _, pending := range owner.pending {
				if pending.route.primary == route.primary && pending.read {
					return nil, UserError("sandbox_busy", "the pull read has not finished delivery", "Wait for its response before acknowledging the fetched tip.")
				}
			}
		}
		if _, used := owner.pending[req.ID]; used || route.finishing >= 2 {
			return nil, pullRouteError("the pull already has pending completion requests")
		}
		route.finishing++
		owner.pending[req.ID] = &pullPending{route: route, token: args.Token}
	}
	return route, nil
}

func (b *bridge) livePullRelay(owner *bridgePull, primary string) (*LivePullRelay, error) {
	b.mu.Lock()
	relay := owner.relays[primary]
	closed := b.pullClosed
	b.mu.Unlock()
	if closed {
		return nil, pullRouteError("the pull connection has ended")
	}
	if relay != nil {
		return relay, nil
	}
	b.mu.Lock()
	retained := len(owner.relays)
	b.mu.Unlock()
	if retained >= api.PullExportsMax {
		return nil, UserError("sandbox_busy", "the prior relay replies are still completing", "Wait for the current pull replies, then retry.")
	}
	created, err := NewLivePullRelay(primary, b.w, b.ownedOutput, b.observePull, &owner.delivery)
	if err != nil {
		return nil, err
	}
	created.SetProgressFactory(b.bindPullProgress)
	b.mu.Lock()
	if b.pullClosed {
		b.mu.Unlock()
		_ = created.Close()
		return nil, pullRouteError("the pull connection ended during its supervisor hello")
	}
	owner.relays[primary] = created
	b.mu.Unlock()
	return created, nil
}

func (b *bridge) observePullError(id uint64, err error) {
	if err != nil {
		b.observePull(proto.Response{ID: id, OK: false})
	}
}

// observePull sees the SAME actual backend response, after its source cleanup.
// It never parses a bundle, creates a source or acquires a primary lease.
func (b *bridge) observePull(response proto.Response) {
	b.mu.Lock()
	defer b.mu.Unlock()
	owner := b.pull
	if owner == nil || b.pullClosed {
		return
	}
	pending, ok := owner.pending[response.ID]
	if !ok {
		return
	} // read replies do not affect export admission
	delete(owner.pending, response.ID)
	if pending.read {
		return
	}
	route := pending.route
	if owner.routes[route.primary] != route {
		return
	}
	if !pending.begin {
		route.finishing--
		// An old-token abort while a full begin is pending has no authority
		// over the new capture. Only its actual token's cleanup frees a slot.
		if (route.beginToken != "" && pending.token == route.beginToken) || (route.beginToken == "" && pending.token == route.token) {
			b.dropPullRoute(owner, route)
		}
		return
	}
	route.beginID, route.beginToken = 0, ""
	route.beginPending = false
	if !response.OK {
		delete(owner.aliases, pending.token)
		if pending.initial {
			b.dropPullRoute(owner, route)
		} else {
			route.epoch = pending.previousEpoch
		}
		return
	}
	var descriptor api.PullExport
	if json.Unmarshal(response.Result, &descriptor) != nil || descriptor.ID != route.original || descriptor.Primary != route.primary || descriptor.Token != pending.token || descriptor.Repo != route.repo {
		// Joining a relay from its reader callback would join itself. End
		// the outer owner asynchronously, which first cancels actual output.
		go b.closePull()
		return
	}
	if descriptor.Empty {
		b.dropPullRoute(owner, route)
		return
	}
	delete(owner.aliases, pending.previous)
	route.token = pending.token
}

func (b *bridge) dropPullRoute(owner *bridgePull, route *pullRoute) {
	delete(owner.routes, route.primary)
	for token, alias := range owner.aliases {
		if alias == route {
			delete(owner.aliases, token)
		}
	}
}

func (b *bridge) retireIdlePullRelays(owner *bridgePull) error {
	b.mu.Lock()
	type retiring struct {
		primary string
		relay   *LivePullRelay
	}
	var idle []retiring
	for primary, relay := range owner.relays {
		if owner.routes[primary] != nil {
			continue
		}
		pending := false
		for _, reply := range owner.pending {
			pending = pending || reply.route.primary == primary
		}
		if !pending {
			idle = append(idle, retiring{primary, relay})
		}
	}
	b.mu.Unlock()
	for _, entry := range idle {
		if err := entry.relay.RetireIdle(); err != nil {
			return err
		}
		// Keep the owned line visible to concurrent whole-channel cleanup
		// until its actual reader has joined. Admission stays on this reader.
		b.mu.Lock()
		if owner.relays[entry.primary] == entry.relay {
			delete(owner.relays, entry.primary)
		}
		b.mu.Unlock()
	}
	return nil
}
