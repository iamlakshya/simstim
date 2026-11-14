package client

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"runtime"
	"slices"
	"sync"
)

// ordinaryLocalGitMetadataSource owns names from the REAL canonical helper,
// never a caller map or reconstructed Settings. Agent modes use the bounded
// adapter to the SAME namespace; this ordinary adapter creates no mode/FIFO.
type ordinaryLocalGitMetadataSource struct {
	mu           sync.Mutex
	operation    *settingsOperation
	names        []string
	owns, closed bool
}

// OpenLocalGitMetadataSource keeps one actual source open until Close. Whole
// canonical settings validation precedes every native role environment lookup.
func OpenLocalGitMetadataSource(ctx context.Context) (_ LocalGitMetadataSource, err error) {
	operation, err := openSettingsOperation(ctx)
	if err != nil {
		return nil, err
	}
	keep := false
	defer func() {
		if !keep {
			err = errors.Join(err, operation.Close())
		}
	}()
	source, err := newOrdinaryLocalGitMetadataSource(ctx, operation)
	if err != nil {
		return nil, err
	}
	source.owns = true
	keep = true
	return source, nil
}

// Original planners call this with their already retained operation. Logical
// borrower Close never closes the planner's original physical source.
func newOrdinaryLocalGitMetadataSource(ctx context.Context, operation *settingsOperation) (*ordinaryLocalGitMetadataSource, error) {
	settings, err := operation.Settings(ctx)
	if err != nil {
		return nil, err
	}
	names, err := ConfiguredSecretNames(settings)
	if err != nil {
		return nil, err
	}
	if err := operation.Revalidate(ctx); err != nil {
		return nil, err
	}
	return &ordinaryLocalGitMetadataSource{operation: operation, names: slices.Sorted(maps.Keys(names))}, nil
}

func (s *ordinaryLocalGitMetadataSource) checkLocked(ctx context.Context) error {
	if s == nil || s.closed || s.operation == nil {
		return localGitMetadataSourceError()
	}
	return s.operation.Revalidate(ctx)
}
func (s *ordinaryLocalGitMetadataSource) Revalidate(ctx context.Context) error {
	if s == nil {
		return localGitMetadataSourceError()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.checkLocked(ctx)
}
func (s *ordinaryLocalGitMetadataSource) CheckKeys(ctx context.Context, keys []string) error {
	if s == nil {
		return localGitMetadataSourceError()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkLocked(ctx); err != nil {
		return err
	}
	for _, key := range keys {
		if checkSecretName(key, true) != secretNameOK {
			return localGitMetadataSourceError()
		}
	}
	// Match the bounded adapter's canonical configured-name order, then the
	// closed role policy's fixed-key order. Only a fixed key is diagnosed.
	for _, name := range s.names {
		for _, key := range keys {
			if metadataNameEqual(name, key, runtime.GOOS == "windows") {
				return &UserError{Msg: fmt.Sprintf("%s is configured as a secret and conflicts with local Git metadata", key), Hint: "Rename the conflicting secret before using local Git metadata.", Code: "git_environment_secret", Kind: KindRefused}
			}
		}
	}
	return s.checkLocked(ctx)
}
func (s *ordinaryLocalGitMetadataSource) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	var err error
	if s.owns {
		err = s.operation.Close()
	}
	s.names, s.operation = nil, nil
	return err
}
func metadataNameEqual(a, b string, windows bool) bool {
	if !windows {
		return a == b
	}
	if len(a) != len(b) {
		return false
	}
	for i := range len(a) {
		if asciiMetadataUpper(rune(a[i])) != asciiMetadataUpper(rune(b[i])) {
			return false
		}
	}
	return true
}

var _ LocalGitMetadataSource = (*ordinaryLocalGitMetadataSource)(nil)
