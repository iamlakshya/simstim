package client

import (
	"context"
	"io"
	"sync/atomic"

	"github.com/iamlakshya/simstim/internal/tomlinspect"
)

type agentMetadataScopeKey struct{}

// Only this actual transaction/snapshot producer issues descriptors. They
// borrow no path, writable file, close method or standalone permission.
type agentMetadataScope struct {
	ctx      context.Context
	control  *agentInspectionCancellation
	mode     *agentModeResources
	settings *agentSettingsSnapshot
	tx       *stateFileTxn
	sources  [2]*agentMetadataSource
	active   atomic.Bool
}

type agentMetadataSource struct {
	scope  *agentMetadataScope
	kind   uint8
	reader io.ReaderAt
	size   int64
}

func newAgentMetadataScope(mode *agentModeResources, settings *agentSettingsSnapshot, tx *stateFileTxn) (*agentMetadataScope, error) {
	if mode == nil || settings == nil || tx == nil || settings.size < 0 || settings.size > MaxConfigSize {
		return nil, agentModeResourceError()
	}
	if err := requireHostMembershipContext(tx.ctx); err != nil {
		return nil, err
	}
	if err := stateContextAuthority(tx.ctx); err != nil {
		return nil, err
	}
	view := tx.view()
	if view.ReaderAt == nil || view.Size < 0 {
		return nil, agentModeResourceError()
	}
	ctx, control, err := newAgentInspectionContext(tx.ctx, mode.ctx)
	if err != nil {
		return nil, err
	}
	scope := &agentMetadataScope{control: control, mode: mode, settings: settings, tx: tx}
	scope.ctx = context.WithValue(ctx, agentMetadataScopeKey{}, scope)
	scope.sources[0] = &agentMetadataSource{scope: scope, kind: tomlinspect.MetadataSettings, reader: settings.reader, size: settings.size}
	// This is the actual guarded aggregate file size, not a new four-MiB
	// aggregate cap. Per-state semantic spans remain checked by one parser.
	scope.sources[1] = &agentMetadataSource{scope: scope, kind: tomlinspect.MetadataSandboxJSON, reader: view.ReaderAt, size: view.Size}
	scope.active.Store(true)
	if err := scope.check(scope.ctx); err != nil {
		scope.close()
		return nil, err
	}
	return scope, nil
}

func (s *agentMetadataScope) check(ctx context.Context) error {
	if s == nil || !s.active.Load() {
		return agentModeResourceError()
	}
	if err := s.ctx.Err(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.mode.checkAgentSettingsMode(s.ctx); err != nil {
		return err
	}
	if err := s.settings.check(); err != nil {
		return err
	}
	return s.tx.check()
}

func (s *agentMetadataScope) descriptors() []tomlinspect.MetadataSource {
	return []tomlinspect.MetadataSource{s.sources[0], s.sources[1]}
}

// Call as a defer INSIDE WithSources' actual inspection callback. It stops
// the merge before that callback returns to region and lease finalisation.
// The scoped lease repeats this on pre-callback failure. Both sources remain
// live for the immediate one-use Allow barrier after the index has closed.
func (s *agentMetadataScope) finishCachedWork() { s.control.finish() }

func (s *agentMetadataScope) close() {
	if s != nil {
		s.control.close()
		s.active.Store(false)
	}
}

func (s *agentMetadataSource) Kind() uint8 { return s.kind }
func (s *agentMetadataSource) Size() int64 { return s.size }

func (s *agentMetadataSource) Check(ctx context.Context) error {
	if s == nil || s.scope == nil {
		return agentModeResourceError()
	}
	if s.kind != tomlinspect.MetadataSettings && s.kind != tomlinspect.MetadataSandboxJSON {
		return agentModeResourceError()
	}
	return s.scope.check(ctx)
}

func (s *agentMetadataSource) ReadAt(out []byte, offset int64) (int, error) {
	if s == nil || s.scope == nil || !s.scope.active.Load() {
		return 0, ErrClosed
	}
	if err := s.scope.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := s.reader.ReadAt(out, offset)
	if !s.scope.active.Load() {
		return n, ErrClosed
	}
	if stopped := s.scope.ctx.Err(); stopped != nil {
		return n, stopped
	}
	return n, err
}

var _ tomlinspect.MetadataSource = (*agentMetadataSource)(nil)
