package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
)

type discoveredInputSeed struct {
	root, name, origin string
	identity           os.FileInfo
}

// inputSeedDiscovery is private read-only source metadata. H5 feeds ONLY its
// primary origin/root to the SAME ResolvePolicyLayers/P29 file/proposal flow,
// before constructing the final original local selection. Secondaries supply
// seeded data and never merge another sandbox-wide policy.
type inputSeedDiscovery struct {
	originalCwd string
	requested   []string
	hostPrimary bool
	seeds       []discoveredInputSeed
}

func discoverSessionSeeds(ctx context.Context, originalCwd string, requested []string, hostPrimary bool, metadata LocalGitMetadataOpener) (_ *inputSeedDiscovery, err error) {
	checker, err := openGitMetadataSource(ctx, []LocalGitMetadataOpener{metadata})
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, checker.Close()) }()
	if err := checker.CheckKeys(ctx, gitMetadataCheckKeys(runtime.GOOS == "windows")); err != nil {
		return nil, err
	}
	if err := checker.Revalidate(ctx); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(originalCwd) {
		return nil, &UserError{Code: "input_cwd", Msg: "the command's original local directory is unavailable",
			Hint: "Run the command again from an ordinary local directory.", Kind: KindFailed}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	result := &inputSeedDiscovery{originalCwd: originalCwd, requested: slices.Clone(requested), hostPrimary: hostPrimary}
	seen := map[string]bool{}
	for _, source := range requested {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		source = expandLocalInputHome(source, home)
		seed, err := discoverInputSeed(ctx, localInputPath(source, originalCwd), metadata)
		if err != nil {
			return nil, err
		}
		if !api.InputRepoName(seed.name) || seen[seed.name] {
			return nil, &UserError{Code: "seed_duplicate", Msg: "two laptop repositories have the same basename",
				Hint: "Choose source folders with distinct basenames.", Kind: KindUsage}
		}
		seen[seed.name] = true
		result.seeds = append(result.seeds, seed)
	}
	return result, ctx.Err()
}

func (d *inputSeedDiscovery) primary() *discoveredInputSeed {
	if d == nil || d.hostPrimary || len(d.seeds) == 0 {
		return nil
	}
	copy := d.seeds[0]
	return &copy
}

func discoverInputSeed(ctx context.Context, source string, metadata ...LocalGitMetadataOpener) (_ discoveredInputSeed, err error) {
	git, err := openPullGit(ctx, source, metadata...)
	if err != nil {
		return discoveredInputSeed{}, err
	}
	defer func() { err = errors.Join(err, git.close()) }()
	name := filepath.Base(git.root)
	identity, err := os.Stat(git.root)
	if err != nil {
		return discoveredInputSeed{}, err
	}
	if oid, err := git.ref(ctx, "HEAD"); err != nil || !api.ValidGitOID(oid) {
		return discoveredInputSeed{}, errors.Join(&UserError{Code: "seed_history", Msg: "a laptop seed has no committed HEAD",
			Hint: "Create an initial commit before seeding this repository.", Kind: KindUsage}, err)
	}
	origin, err := git.run(ctx, nil, 64<<10, "config", "--get", "remote.origin.url")
	if gitExit(err, 1) {
		origin, err = nil, nil
	}
	if err != nil {
		return discoveredInputSeed{}, err
	}
	after, err := os.Stat(git.root)
	if err != nil || !os.SameFile(identity, after) {
		return discoveredInputSeed{}, copyInputError("seed_changed", "the source repository changed during discovery")
	}
	return discoveredInputSeed{root: git.root, name: name, origin: gitLine(origin), identity: identity}, ctx.Err()
}
