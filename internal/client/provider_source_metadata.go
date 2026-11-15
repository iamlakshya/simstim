package client

import (
	"context"
	"os"
	"runtime"

	"github.com/iamlakshya/simstim/internal/api"
)

// ProviderSourceMetadata borrows the SAME canonical closed metadata-role
// policy and actual settings namespace. It accepts no caller key/name map.
// It runs no configured secret command and creates no provider resource.
type ProviderSourceMetadata struct{ metadata *localGitMetadata }

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
	return s.metadata.environment(ctx, localGitNormal, "", api.GitIdentity{})
}

// Revalidate checks the same held settings source and mode without renewal.
func (s *ProviderSourceMetadata) Revalidate(ctx context.Context) error {
	if s == nil || s.metadata == nil {
		return gitMetadataFailure()
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
