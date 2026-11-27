package host

import (
	"context"
	"encoding/json"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/pullexport"
)

// PullRPC is the one domain RPC implementation shared by the bridge's stopped
// path and the actual live supervisor. It adds no framing or process engine.
type PullRPC struct {
	life   context.Context
	cancel context.CancelFunc
	owner  *pullexport.Manager
}

// NewPullRPC owns at most four exports of this authenticated connection.
// Its opener is the actual canonical primary resource owner in that process.
func NewPullRPC(ctx context.Context, open PullSourceOpener) (*PullRPC, error) {
	life, cancel := context.WithCancel(ctx)
	manager, err := pullexport.New(life, pullexport.Options{Directory: pullSpoolDir, Open: pullexport.Open(open)})
	if err != nil {
		cancel()
		return nil, err
	}
	return &PullRPC{life: life, cancel: cancel, owner: manager}, nil
}

// IsPullOp selects only the literal normal pull RPCs.
func IsPullOp(op string) bool {
	return op == api.OpPullBegin || op == api.OpPullRead || op == api.OpPulled
}

// Prepare must run synchronously on the canonical authenticated request reader,
// in frame order, before scheduling run. Begin's nonce is reserved here. Later
// aborts therefore cannot run ahead of admission. Close reclaims even a reserved
// run that was never scheduled. Every returned run is tied to this lifetime.
func (p *PullRPC) Prepare(req proto.Request) (run func(context.Context) (any, error), err error) {
	bad := func() error {
		return UserError("pull_protocol", "the pull request is invalid", "Use the current client and a full session address.")
	}
	const connectionOwner = "connection"
	switch req.Op {
	case api.OpPullBegin:
		var args api.PullBeginArgs
		if !api.ExactJSONFields(req.Args, args) || json.Unmarshal(req.Args, &args) != nil {
			return nil, bad()
		}
		primary, err := SandboxPrimaryID(args.ID)
		if err != nil {
			return nil, err
		}
		manifest, err := LoadManifest(primary)
		if err != nil {
			return nil, err
		}
		limit, err := EffectivePullMax(manifest)
		if err != nil {
			return nil, err
		}
		capture, err := p.owner.PrepareBeginRequest(p.life, connectionOwner, primary, args, limit, req.ID)
		if err != nil {
			return nil, err
		}
		return func(ctx context.Context) (any, error) { return capture(ctx) }, nil
	case api.OpPullRead:
		var args api.PullReadArgs
		if json.Unmarshal(req.Args, &args) != nil {
			return nil, bad()
		}
		read, err := p.owner.PrepareReadRequest(p.life, connectionOwner, args, req.ID)
		if err != nil {
			return nil, err
		}
		return func(ctx context.Context) (any, error) { return read(ctx) }, nil
	case api.OpPulled:
		var args api.PulledArgs
		if json.Unmarshal(req.Args, &args) != nil {
			return nil, bad()
		}
		return func(ctx context.Context) (any, error) { return p.owner.Finish(ctx, connectionOwner, args) }, nil
	default:
		return nil, bad()
	}
}

// Context is the exact authenticated outer connection lifetime.
func (p *PullRPC) Context() context.Context { return p.life }

// Touch accepts only actual output acknowledgements on the same owned line.
func (p *PullRPC) Touch(progress api.PullOutputProgress) bool {
	return p.owner.Touch(p.life, "connection", progress)
}

// Close cancels and joins every owned export before the connection releases
// its supervisor, primary lease or actual backend resource.
func (p *PullRPC) Close() { p.cancel(); p.owner.Close() }
