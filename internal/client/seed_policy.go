package client

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/configdata"
	"github.com/iamlakshya/simstim/internal/repopolicy"
)

// seedPolicySnapshot binds policy to one actual prospective seeded commit.
// The temporary dirty index is private and its actual native owner must join
// before cleanup. The source's index, refs and worktree are never rewritten.
type seedPolicySnapshot struct {
	head, commit, parent, base, blob, hash string
	dirty                                  bool
	ready                                  bool
	policy                                 *api.RepositoryPolicy
	scratch                                string
	untracked                              []string
	identity                               api.GitIdentity
	ownership                              *localGitOwnership
	metadata                               LocalGitMetadataOpener
}

// primaryPolicy is called before the canonical P29 resolution and any value
// commands. base is the original explicit/local selected base, or empty for
// HEAD. The SAME original settings operation supplies metadata. Secondaries
// never supply another sandbox policy.
func (d *inputSeedDiscovery) primaryPolicy(ctx context.Context, dirty bool, base string, metadata LocalGitMetadataOpener) (_ *api.RepositoryPolicy, err error) {
	if d == nil || d.hostPrimary || len(d.seeds) == 0 {
		return nil, nil
	}
	seed := &d.seeds[0]
	if seed.policy != nil {
		if seed.policy.dirty != dirty || seed.policy.base != base {
			return nil, copyInputError("seed_changed", "the primary policy selection changed")
		}
		if err := d.revalidatePrimaryPolicy(ctx); err != nil {
			return nil, err
		}
		return seed.policy.policy, nil
	}
	git, err := openPullGit(ctx, seed.root, metadata)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, git.close()) }()
	head, err := git.ref(ctx, "HEAD")
	if err != nil {
		return nil, err
	}
	if head != seed.head {
		return nil, copyInputError("seed_changed", "the primary HEAD changed after discovery")
	}
	ref := base
	if ref == "" {
		ref = "HEAD"
	}
	commit, err := git.ref(ctx, ref)
	if err != nil {
		return nil, err
	}
	snapshot := &seedPolicySnapshot{head: head, commit: commit, parent: commit, base: base, dirty: dirty, ownership: git.ownership, metadata: metadata}
	// Attach before the first write so errors retain the actual cleanup owner.
	seed.policy = snapshot
	if dirty {
		snapshot.identity, err = readCommandGitIdentity(ctx, d.originalCwd, metadata)
		if err != nil {
			return nil, err
		}
		snapshot.scratch, err = os.MkdirTemp("", "laptop-policy-")
		if err != nil {
			return nil, err
		}
		snapshot.commit, snapshot.untracked, err = dirtySeedCommit(ctx, git, snapshot.scratch, commit, snapshot.identity)
		if err != nil {
			return nil, err
		}
	}
	snapshot.policy, snapshot.blob, err = readSeedPolicy(ctx, git, seed.name, snapshot.commit)
	if err != nil {
		return nil, err
	}
	if snapshot.policy != nil {
		snapshot.hash = snapshot.policy.Hash
	}
	snapshot.ready = true
	if err = d.revalidatePrimaryPolicy(ctx); err != nil {
		return nil, err
	}
	return snapshot.policy, nil
}

// revalidatePrimaryPolicy checks the original source and exact selected blob
// before value commands or first payload; it never reselects another policy.
func (d *inputSeedDiscovery) revalidatePrimaryPolicy(ctx context.Context) (err error) {
	if d == nil || d.hostPrimary || len(d.seeds) == 0 {
		return nil
	}
	seed := d.seeds[0]
	snapshot := seed.policy
	if snapshot == nil || !snapshot.ready {
		return copyInputError("seed_policy", "the selected primary policy snapshot is missing or incomplete")
	}
	info, err := os.Stat(seed.root)
	if err != nil {
		return err
	}
	if !os.SameFile(info, seed.identity) {
		return copyInputError("seed_changed", "the primary source changed after policy selection")
	}
	git, err := openPullGit(ctx, seed.root, snapshot.metadata)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, git.close()) }()
	head, err := git.ref(ctx, "HEAD")
	if err != nil {
		return err
	}
	if head != snapshot.head {
		return copyInputError("seed_changed", "the primary HEAD changed after policy selection")
	}
	if snapshot.base != "" {
		base, err := git.ref(ctx, snapshot.base)
		if err != nil {
			return err
		}
		if base != snapshot.parent {
			return copyInputError("seed_changed", "the selected primary base changed after policy selection")
		}
	}
	policy, blob, err := readSeedPolicy(ctx, git, seed.name, snapshot.commit)
	if err != nil {
		return err
	}
	hash := ""
	if policy != nil {
		hash = policy.Hash
	}
	if blob != snapshot.blob || hash != snapshot.hash {
		return copyInputError("seed_changed", "the selected primary policy changed")
	}
	return ctx.Err()
}

// Close releases only prospective private dirty-index scratch. A failed join
// retains it and remains retryable; the captured commit is an unreachable Git
// object and no source ref is ever removed or changed here.
func (d *inputSeedDiscovery) Close() error {
	if d == nil {
		return nil
	}
	var failures []error
	for _, seed := range d.seeds {
		if snapshot := seed.policy; snapshot != nil && snapshot.scratch != "" {
			if err := snapshot.ownership.quiescent(); err != nil {
				failures = append(failures, err)
				continue
			}
			if err := os.RemoveAll(snapshot.scratch); err != nil {
				failures = append(failures, err)
			} else {
				snapshot.scratch = ""
			}
		}
	}
	return errors.Join(failures...)
}

func readSeedPolicy(ctx context.Context, git *pullGit, repo, commit string) (*api.RepositoryPolicy, string, error) {
	if !api.ValidGitOID(commit) {
		return nil, "", copyInputError("seed_policy", "the selected primary commit is invalid")
	}
	directory := "." + brand.Name
	mode, kind, _, err := seedPolicyEntry(ctx, git, commit, directory)
	if err != nil {
		return nil, "", err
	}
	if mode == "" {
		return nil, "", nil
	}
	if mode != "040000" || kind != "tree" {
		return nil, "", copyInputError("repo_policy", "the selected repository policy folder is not a real tree")
	}
	mode, kind, blob, err := seedPolicyEntry(ctx, git, commit, directory+"/sandbox.toml")
	if err != nil {
		return nil, "", err
	}
	if mode == "" {
		return nil, "", nil
	}
	if mode != "100644" && mode != "100755" || kind != "blob" {
		return nil, "", copyInputError("repo_policy", "the selected repository policy is not a regular file")
	}
	data, err := git.run(ctx, nil, configdata.MaxFileSize, "cat-file", "blob", blob)
	if err != nil {
		return nil, "", err
	}
	policy, err := repopolicy.Decode(repo, data)
	return policy, blob, err
}

func seedPolicyEntry(ctx context.Context, git *pullGit, commit, path string) (string, string, string, error) {
	data, err := git.run(ctx, nil, 4096, "ls-tree", "-z", commit, "--", path)
	if err != nil {
		return "", "", "", err
	}
	if len(data) == 0 {
		return "", "", "", nil
	}
	if data[len(data)-1] != 0 || bytes.Count(data, []byte{0}) != 1 {
		return "", "", "", copyInputError("seed_policy", "Git returned an ambiguous primary policy entry")
	}
	header, name, ok := strings.Cut(string(data[:len(data)-1]), "\t")
	parts := strings.Split(header, " ")
	if !ok || name != path || len(parts) != 3 || !api.ValidGitOID(parts[2]) {
		return "", "", "", copyInputError("seed_policy", "Git returned an invalid primary policy entry")
	}
	return parts[0], parts[1], parts[2], nil
}
