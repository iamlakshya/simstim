package client

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
)

// lookupGit uses the captured, canonically checked PATH only. It never reads
// ambient PATH/PATHEXT or searches the current directory or relative entries.
func (m *localGitMetadata) lookupGit(ctx context.Context, _ string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return "", gitMetadataFailure()
	}
	if err := m.source.Revalidate(ctx); err != nil {
		return "", err
	}
	path, _ := m.value("PATH")
	return resolveGitMetadataPath(ctx, path, m.windows)
}

func resolveGitMetadataPath(ctx context.Context, path string, windows bool) (string, error) {
	name := "git"
	if windows {
		name = "git.exe"
	}
	for _, directory := range filepath.SplitList(path) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if directory == "" || !filepath.IsAbs(directory) {
			continue
		}
		candidate := filepath.Join(directory, name)
		info, err := os.Stat(candidate)
		if err != nil || !info.Mode().IsRegular() || !windows && info.Mode().Perm()&0111 == 0 {
			continue
		}
		return candidate, nil
	}
	return "", &exec.Error{Name: "git", Err: exec.ErrNotFound}
}
