package client

import (
	"context"
	"fmt"
	"slices"
	"sync"
)

// LocalGitMetadataSource checks the original local configuration namespace.
// It grants no value fetch, source selection, environment or process authority.
// CheckKeys receives only the closed native environment policy's fixed keys.
type LocalGitMetadataSource interface {
	CheckKeys(context.Context, []string) error
	Revalidate(context.Context) error
	Close() error
}

// This lease borrows the actual original snapshot. Its operation owner keeps
// the physical source open until every native operation and borrower is done.
// There is no constructor from Settings{}, a name map, a row or a barrier.
type localGitMetadataNames struct {
	mu        sync.Mutex
	inspector *agentSettingsInspector
	mode      *agentModeResources
	snapshot  *agentSettingsSnapshot
	closed    bool
}

func newLocalGitMetadataNames(ctx context.Context, inspector *agentSettingsInspector, snapshot *agentSettingsSnapshot) (*localGitMetadataNames, error) {
	if inspector == nil || inspector.mode == nil || inspector.engine == nil || inspector.visitor.schema == nil || snapshot == nil {
		return nil, localGitMetadataSourceError()
	}
	mode, ok := inspector.mode.(*agentModeResources)
	if !ok {
		return nil, localGitMetadataSourceError()
	}
	if err := inspector.mode.checkAgentSettingsMode(ctx); err != nil {
		return nil, err
	}
	if err := snapshot.check(); err != nil {
		return nil, agentSettingsFailure(err)
	}
	return &localGitMetadataNames{inspector: inspector, mode: mode, snapshot: snapshot}, nil
}

func (s *localGitMetadataNames) checkLocked(ctx context.Context) error {
	if s == nil || s.closed || s.inspector == nil || s.snapshot == nil {
		return localGitMetadataSourceError()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.inspector.mode.checkAgentSettingsMode(ctx); err != nil {
		return err
	}
	return s.snapshot.check()
}

func (s *localGitMetadataNames) CheckKeys(ctx context.Context, fixedKeys []string) error {
	if s == nil {
		return localGitMetadataSourceError()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkLocked(ctx); err != nil {
		return agentSettingsFailure(err)
	}
	if ctx.Value(agentMetadataScopeKey{}) != nil {
		return localGitMetadataSourceError()
	}
	inspectionCtx, control, err := newAgentInspectionContext(ctx, s.mode.ctx)
	if err != nil {
		return err
	}
	defer control.close()
	keys := slices.Clone(fixedKeys)
	for _, key := range keys {
		if checkSecretName(key, true) != secretNameOK {
			return localGitMetadataSourceError()
		}
	}
	visitor := metadataNameVisitor{agentSettingsVisitor: agentSettingsVisitor{schema: s.inspector.visitor.schema}, keys: keys}
	if err := s.inspector.engine.Inspect(inspectionCtx, s.snapshot.reader, s.snapshot.size, &visitor); err != nil {
		return agentSettingsFailure(err)
	}
	if err := s.checkLocked(ctx); err != nil {
		return agentSettingsFailure(err)
	}
	if visitor.collision != "" {
		return &UserError{Msg: fmt.Sprintf("%s is configured as a secret and conflicts with local Git metadata", visitor.collision),
			Hint: "Rename the conflicting secret before using local Git metadata.", Code: "git_environment_secret", Kind: KindRefused}
	}
	return nil
}

func (s *localGitMetadataNames) Revalidate(ctx context.Context) error {
	if s == nil {
		return localGitMetadataSourceError()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.checkLocked(ctx)
}

func (s *localGitMetadataNames) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	s.inspector, s.snapshot, s.mode = nil, nil, nil
	return nil
}

func localGitMetadataSourceError() error {
	return &UserError{Msg: "the original local secret-name namespace is unavailable for Git metadata",
		Hint: "Start the operation through its original local configuration snapshot.", Code: "source_inventory", Kind: KindRefused}
}

var _ LocalGitMetadataSource = (*localGitMetadataNames)(nil)
