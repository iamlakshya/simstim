package client

import (
	"context"
	"os"
	"runtime"
	"sync/atomic"

	"github.com/iamlakshya/simstim/internal/api"
)

// ProviderSourceMetadata borrows the SAME canonical closed metadata-role
// policy and actual settings namespace. It accepts no caller key/name map.
// It runs no configured secret command and creates no provider resource.
type ProviderSourceMetadata struct {
	metadata *localGitMetadata
	limaHome atomic.Bool
}

// OpenProviderSourceMetadata opens the actual canonical namespace and captures
// the fixed metadata environment after its complete name collision check.
func OpenProviderSourceMetadata(ctx context.Context) (*ProviderSourceMetadata, error) {
	source, err := OpenLocalGitMetadataSource(ctx)
	if err != nil {
		return nil, err
	}
	metadata, err := captureGitMetadata(ctx, source, os.LookupEnv, runtime.GOOS == "windows")
	if err != nil {
		return nil, err
	}
	return &ProviderSourceMetadata{metadata: metadata}, nil
}

// Environment returns an owned copy of only the closed metadata role.
func (s *ProviderSourceMetadata) Environment(ctx context.Context) ([]string, error) {
	if s == nil || s.metadata == nil {
		return nil, gitMetadataFailure()
	}
	if s.limaHome.Load() {
		if err := s.checkLimaHome(ctx); err != nil {
			return nil, err
		}
	}
	return s.metadata.environment(ctx, localGitNormal, "", api.GitIdentity{})
}

// Revalidate checks the same held settings source and mode without renewal.
func (s *ProviderSourceMetadata) Revalidate(ctx context.Context) error {
	if s == nil || s.metadata == nil {
		return gitMetadataFailure()
	}
	if s.limaHome.Load() {
		if err := s.checkLimaHome(ctx); err != nil {
			return err
		}
	}
	return s.metadata.Revalidate(ctx)
}

// Close releases this operation's actual namespace lease.
func (s *ProviderSourceMetadata) Close() error {
	if s == nil || s.metadata == nil {
		return nil
	}
	return s.metadata.Close()
}

// ReserveLimaHome checks the ONE additional fixed derived locator name before
// the actual Lima provider appends its original-source LIMA_HOME. It reads no
// ambient LIMA_HOME and opens no second settings or name source.
func (s *ProviderSourceMetadata) ReserveLimaHome(ctx context.Context) error {
	if err := s.checkLimaHome(ctx); err != nil {
		return err
	}
	s.limaHome.Store(true)
	return nil
}
func (s *ProviderSourceMetadata) checkLimaHome(ctx context.Context) error {
	if s == nil || s.metadata == nil {
		return gitMetadataFailure()
	}
	m := s.metadata
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.source == nil {
		return gitMetadataFailure()
	}
	if err := m.source.CheckKeys(ctx, []string{"LIMA_HOME"}); err != nil {
		return err
	}
	return m.source.Revalidate(ctx)
}
