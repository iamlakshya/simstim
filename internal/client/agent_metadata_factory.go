package client

import (
	"context"
	"sync"

	"github.com/iamlakshya/simstim/internal/tomlinspect"
)

func (m *agentModeResources) AcquireSources(ctx context.Context, sources []tomlinspect.MetadataSource) (tomlinspect.IndexLease, error) {
	if len(sources) != 2 {
		return nil, agentModeResourceError()
	}
	settings, ok := sources[0].(*agentMetadataSource)
	if !ok || settings.scope == nil {
		return nil, agentModeResourceError()
	}
	scope := settings.scope
	if scope.mode != m || ctx.Value(agentMetadataScopeKey{}) != scope ||
		sources[0] != scope.sources[0] || sources[1] != scope.sources[1] {
		return nil, agentModeResourceError()
	}
	if err := scope.check(ctx); err != nil {
		return nil, err
	}
	// Source roles were admitted by the actual producer. Physical admission
	// reuses the SAME serial gate, anonymous file and slab, with no aggregate
	// TOML-size limit or reservation based on the entire historical file.
	lease, err := m.acquireIndex(ctx)
	if err != nil {
		return nil, err
	}
	return &agentScopedIndexLease{IndexLease: lease, scope: scope}, nil
}

type agentScopedIndexLease struct {
	tomlinspect.IndexLease
	scope *agentMetadataScope
	once  sync.Once
	err   error
}

func (l *agentScopedIndexLease) Close() error {
	l.once.Do(func() {
		l.scope.finishCachedWork()
		l.err = l.IndexLease.Close()
	})
	return l.err
}

var _ tomlinspect.SourceIndexFactory = (*agentModeResources)(nil)
