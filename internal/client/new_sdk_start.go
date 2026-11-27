package client

import (
	"context"
	"maps"
	"reflect"
	"slices"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
)

// sdkNewStartPlanV1 retains only the original raw SDK choice. Actual input,
// runtime and secret captures remain owned by their existing private plans.
type sdkNewStartPlanV1 struct {
	mu        sync.Mutex
	request   *GrantRequest
	conn      *Conn
	operation *settingsOperation
	original  NewArgs
	busy      bool
	closed    bool
}

func cloneSDKNewArgsV1(a NewArgs) NewArgs {
	a.Argv = slices.Clone(a.Argv)
	a.Mounts = slices.Clone(a.Mounts)
	a.Include = slices.Clone(a.Include)
	a.Secrets = maps.Clone(a.Secrets)
	a.SetupTrust = maps.Clone(a.SetupTrust)
	a.SetupHashes = maps.Clone(a.SetupHashes)
	return a
}

func (p *sdkNewStartPlanV1) close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.busy {
		return agentModeResourceError()
	}
	p.closed = true
	p.original = NewArgs{}
	p.request, p.conn, p.operation = nil, nil, nil
	return nil
}

func sdkNewStartReplayV1(original, actual NewArgs) (NewArgs, bool) {
	if original.MakeFolder && !actual.MakeFolder || actual.Setup != "" && actual.Setup != api.SetupRun && actual.Setup != api.SetupSkip || original.Setup != "" && actual.Setup != original.Setup {
		return actual, false
	}
	core := cloneSDKNewArgsV1(actual)
	core.MakeFolder, core.Setup = original.MakeFolder, original.Setup
	if original.Setup == "" {
		core.SetupTrust, core.SetupHashes = original.SetupTrust, original.SetupHashes
	}
	if !reflect.DeepEqual(original, core) {
		return actual, false
	}
	replay := cloneSDKNewArgsV1(original)
	replay.MakeFolder, replay.Setup = actual.MakeFolder, actual.Setup
	if original.Setup == "" {
		replay.SetupTrust, replay.SetupHashes = maps.Clone(actual.SetupTrust), maps.Clone(actual.SetupHashes)
	}
	return replay, true
}

func sdkNewStartPlanForV1(ctx context.Context, c *Conn, a NewArgs) (*sdkNewStartPlanV1, error) {
	request := grantRequestOf(ctx)
	if request == nil || sdkNewHasPreparedDTO(a) || len(a.Secrets) != 0 {
		return nil, grantOperationError()
	}
	if err := requireGrantOperation(ctx, grantOpNew); err != nil {
		return nil, err
	}
	operation, err := request.borrowNewSettings(ctx)
	if err != nil {
		return nil, err
	}
	request.mu.Lock()
	defer request.mu.Unlock()
	if request.preparationClosed || request.ending {
		return nil, grantOperationError()
	}
	if request.sdkStart == nil {
		if request.wall != nil || request.newInputs != nil || request.newSecrets != nil {
			return nil, grantOperationError()
		}
		request.sdkStart = &sdkNewStartPlanV1{request: request, conn: c, operation: operation, original: cloneSDKNewArgsV1(a)}
	}
	p := request.sdkStart
	p.mu.Lock()
	defer p.mu.Unlock()
	_, sameChoice := sdkNewStartReplayV1(p.original, a)
	if p.closed || p.busy || p.request != request || p.conn != c || p.operation != operation || !sameChoice {
		return nil, grantOperationError()
	}
	p.busy = true
	return p, nil
}

// prepareSDKNewStartV1 gives an ordinary SDK start the same original local
// policy, input and secret preparation as a frontend start. The caller owns
// the request and its lifetime; this function never replaces that request.
func prepareSDKNewStartV1(ctx context.Context, c *Conn, a NewArgs) (NewArgs, error) {
	plan, err := sdkNewStartPlanForV1(ctx, c, a)
	if err != nil {
		return a, err
	}
	defer func() { plan.mu.Lock(); plan.busy = false; plan.mu.Unlock() }()
	a, _ = sdkNewStartReplayV1(plan.original, a)
	request := plan.request
	intent := a.Wall
	if intent == "" {
		intent = "auto"
	}
	values := api.LayerValues{Sandbox: api.SandboxValues{Mounts: slices.Clone(a.Mounts), Include: slices.Clone(a.Include)}}
	if a.Wall != "" {
		level := a.Wall
		values.Sandbox.Wall = &level
	}
	options := NewInputOptions{HostClone: a.Cwd != "", Session: values}
	a, err = PrepareNewSessionArgs(ctx, a, a.Cols, a.Rows)
	if err != nil {
		return a, err
	}
	a, err = c.PrepareNewInputPolicy(ctx, a, options)
	if err != nil {
		return a, err
	}
	a, err = c.PrepareNewPolicy(ctx, a)
	if err != nil {
		return a, err
	}
	a, err = c.ResolveNewWall(ctx, a)
	if err != nil {
		return a, err
	}
	if err := request.BindNewWall(ctx, a.Wall, nil); err != nil {
		return a, err
	}
	a, err = c.PrepareNewInputs(ctx, a)
	if err != nil {
		return a, err
	}
	var vm *VMNeedPlanV1
	if a.Wall == "microvm" {
		vm, err = c.PrepareNewVM(ctx, a, intent)
		if err != nil {
			return a, err
		}
	}
	// A retry uses the actual prepared header's memory spelling. Preserve the
	// original auto sentinel until first preparation, where it overrides a
	// configured cap rather than being mistaken for an unspecified zero.
	if prepared := newPlanOf(ctx); prepared != nil {
		prepared.mu.Lock()
		a.MemoryMax = prepared.header.MemoryMax
		prepared.mu.Unlock()
	}
	if vm != nil {
		a, err = c.PrepareNewSecrets(ctx, a, vm)
	} else {
		a, err = c.PrepareNewSecrets(ctx, a)
	}
	if err != nil {
		return a, err
	}
	settings, err := newOperationSettings(ctx)
	if err != nil {
		return a, err
	}
	selection, err := selectPreparedNewAgent(ctx, settings, c, a)
	if err != nil {
		return a, err
	}
	names := selection.Names()
	if err := c.CheckNewGrants(ctx, names, a.SSHAgent); err != nil {
		return a, err
	}
	if err := c.CheckNewPolicyGrants(ctx, a.Layers); err != nil {
		return a, err
	}
	a.Secrets, err = FetchHostSecrets(ctx, c.Host, names, false)
	return a, err
}

// PrepareUnwalledControlStartV1 prepares an explicitly unwalled installer or
// login on the accepted host. It does not select sandbox inputs or fetch any
// configured credentials. Keep the same request through NewSession and Close.
func (c *Conn) PrepareUnwalledControlStartV1(ctx context.Context, a NewArgs) (NewArgs, error) {
	request := grantRequestOf(ctx)
	if request == nil || a.Wall != "none" || !a.Downgrade || sdkNewHasPreparedDTO(a) ||
		len(a.Secrets) != 0 || a.SSHAgent || request.SSHAgent || len(a.Mounts) != 0 || len(a.Include) != 0 || a.Worktree || a.Base != "" {
		return a, grantOperationError()
	}
	if err := requireGrantOperation(ctx, grantOpNew); err != nil {
		return a, err
	}
	operation, err := request.borrowNewSettings(ctx)
	if err != nil {
		return a, err
	}
	if _, err := operation.Settings(ctx); err != nil {
		return a, err
	}
	a, err = c.ResolveNewWall(ctx, a)
	if err != nil {
		return a, err
	}
	if err := request.BindNewWall(ctx, a.Wall, nil); err != nil {
		return a, err
	}
	return a, operation.Revalidate(ctx)
}

func sdkNewHasPreparedDTO(a NewArgs) bool {
	return a.Placement != nil || a.Runtime != nil || len(a.VMNeedV1) != 0 || a.Inputs != nil || a.Environment != nil ||
		a.SecretDelivery != nil || a.NamedSSHKeys != nil || len(a.ProtectedTrustedSecretValues) != 0 ||
		a.RepositoryPolicy != nil || len(a.Layers) != 0 || a.GrantHash != "" || len(a.GrantAnswers) != 0 || a.ID != "" || a.HostLabel != ""
}
