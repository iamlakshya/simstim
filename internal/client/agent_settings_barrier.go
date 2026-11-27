package client

import (
	"context"
	"errors"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/tomlinspect"
)

// Only the actual client mode owner can implement this private guard.
// The resource factory and the mode identity are one capability, never a
// callback/provider fallback selected from configuration or environment.
type agentSettingsMode interface {
	tomlinspect.IndexFactory
	checkAgentSettingsMode(context.Context) error
}

type agentSettingsInspector struct {
	mode    agentSettingsMode
	engine  *tomlinspect.Inspector
	visitor agentSettingsVisitor
}

func newAgentSettingsInspector(mode agentSettingsMode) (*agentSettingsInspector, error) {
	engine, err := tomlinspect.NewInspector(mode)
	if err != nil {
		return nil, err
	}
	schema, err := newAgentSchema()
	if err != nil {
		return nil, err
	}
	return &agentSettingsInspector{mode: mode, engine: engine, visitor: agentSettingsVisitor{schema: schema}}, nil
}

// This seal exists only on the immediate full-validation→Allow call stack.
// It has no Settings/view/command payload, cannot outlive the open snapshot,
// and is consumed once by the ONE shared outbound dispatch.
type agentSettingsBarrier struct {
	ctx             context.Context
	mode            agentSettingsMode
	snapshot        *agentSettingsSnapshot
	scope           *agentMetadataScope
	validated, used bool
}

func (b *agentSettingsBarrier) check() error {
	if b == nil || !b.validated || b.used || b.ctx == nil || b.mode == nil || b.snapshot == nil {
		return validatedConfigScopeError()
	}
	if err := b.ctx.Err(); err != nil {
		return err
	}
	if err := b.mode.checkAgentSettingsMode(b.ctx); err != nil {
		return err
	}
	if err := b.snapshot.check(); err != nil {
		return agentSettingsFailure(err)
	}
	if b.scope != nil {
		if b.scope.settings != b.snapshot || b.scope.mode != b.mode {
			return validatedConfigScopeError()
		}
		if err := b.scope.check(b.ctx); err != nil {
			return err
		}
	}
	b.used = true
	return nil
}

func (i *agentSettingsInspector) allow(ctx context.Context, h Host, t Tier, outbound Outbound) (err error) {
	if i == nil || i.mode == nil || i.engine == nil || ctx == nil {
		return validatedConfigScopeError()
	}
	if err := i.mode.checkAgentSettingsMode(ctx); err != nil {
		return err
	}
	snapshot, err := openAgentSettingsSnapshot(ctx)
	if err != nil {
		return agentSettingsFailure(err)
	}
	barrier := agentSettingsBarrier{ctx: ctx, mode: i.mode, snapshot: snapshot}
	defer func() {
		barrier.validated = false
		if failure := snapshot.close(); failure != nil {
			err = errors.Join(err, agentSettingsFailure(failure))
		}
	}()
	if err := i.engine.Inspect(ctx, snapshot.reader, snapshot.size, &i.visitor); err != nil {
		return agentSettingsFailure(err)
	}
	if err := snapshot.check(); err != nil {
		return agentSettingsFailure(err)
	}
	barrier.validated = true
	return allowWithValidatedConfig(h, t, outbound, nil, nil, &barrier)
}

func agentSettingsFailure(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var user *UserError
	if errors.As(err, &user) {
		return err
	}
	if errors.Is(err, tomlinspect.ErrIndexIO) || errors.Is(err, tomlinspect.ErrIndexCorrupt) || errors.Is(err, tomlinspect.ErrIndexInvariant) {
		return agentModeResourceError()
	}
	return &UserError{Msg: "config.toml cannot be read or validated for this SSH-agent grant", Hint: brand.Name + " config check", Code: "bad_config", Kind: KindUsage}
}
