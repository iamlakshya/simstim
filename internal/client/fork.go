package client

import (
	"context"

	"github.com/iamlakshya/simstim/internal/api"
)

// Fork copies a sandbox into independent cold starts. Each failed child
// admission remains in the result beside the children that actually started.
func (c *Conn) Fork(ctx context.Context, args api.ForkArgs) (api.ForkResult, error) {
	if args.Count == 0 {
		args.Count = 1
	}
	if err := args.Validate(); err != nil {
		return api.ForkResult{}, &UserError{Code: "bad_fork", Msg: "invalid fork session, name or count", Hint: "Choose a session, a valid name and a count from 1 to 32.", Kind: KindUsage}
	}
	if c == nil || !c.HasFeature(api.FeatureFork) {
		return api.ForkResult{}, forkCheckpointUpgrade("fork sandboxes")
	}
	var result api.ForkResult
	if err := c.Call(ctx, api.OpFork, args, &result); err != nil {
		return result, err
	}
	if err := result.Validate(args); err != nil {
		return api.ForkResult{}, forkCheckpointProtocol()
	}
	return result, nil
}

func forkCheckpointUpgrade(action string) error {
	return &UserError{Code: "helper_old", Msg: "this host helper cannot " + action, Hint: "Update the host helper, then try again.", Kind: KindFailed}
}

func forkCheckpointProtocol() error {
	return &UserError{Code: "protocol", Msg: "the host returned invalid fork or checkpoint metadata", Hint: "Update the client and host helper, then list the session again.", Kind: KindFailed}
}

// ForkFailureError uses the ordinary cleaned host error mapping.
func ForkFailureError(failure api.ForkFailure) error { return hostError(&failure.Error) }
