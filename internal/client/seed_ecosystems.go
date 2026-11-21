package client

import (
	"context"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/seedmeta"
)

// readSeedEcosystems uses the existing owned Git operation for the same
// selected commit as the bundle. It reads names, never manifest contents.
func readSeedEcosystems(ctx context.Context, git *pullGit, commit string) ([]string, error) {
	if git == nil || !api.ValidGitOID(commit) {
		return nil, copyInputError("seed_metadata", "seed ecosystem facts have no selected commit")
	}
	args := append([]string{"ls-tree", "-z", "--name-only", commit, "--"}, seedmeta.RootManifests()...)
	listing, err := git.run(ctx, nil, seedmeta.MaxListingBytes, args...)
	if err != nil {
		return nil, err
	}
	return seedmeta.Ecosystems(listing)
}
