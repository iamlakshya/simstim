package guest

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/gitpolicy"
	"github.com/iamlakshya/simstim/internal/proto"
)

func purgeWorkErrorV1(message string) error {
	return &proto.Error{Code: "purge_work_unknown", Message: message, Hint: "Keep the sandbox, pull its committed work and copy its remaining files before retrying purge."}
}

func decodePurgeWorkV1(raw json.RawMessage) (proto.PurgeWorkInspectArgsV1, error) {
	var args proto.PurgeWorkInspectArgsV1
	if len(raw) > 48<<10 || !api.ExactJSONFields(raw, args) || json.Unmarshal(raw, &args) != nil || args.Version != 1 || !api.ValidSessionID(args.Primary) || len(args.Repositories) == 0 || len(args.Repositories) > 128 {
		return args, purgeWorkErrorV1("the workspace inspection has no complete protected repository plan")
	}
	seen := map[string]bool{}
	for _, repo := range args.Repositories {
		if !api.InputRepoName(repo.Name) || seen[repo.Name] {
			return args, purgeWorkErrorV1("the workspace inspection changed a protected repository name")
		}
		seen[repo.Name] = true
		commits := map[string]bool{}
		for _, oid := range repo.Saved {
			if !api.ValidGitOID(oid) || commits[oid] {
				return args, purgeWorkErrorV1("the workspace inspection has invalid saved commit data")
			}
			commits[oid] = true
		}
	}
	return args, nil
}

// inspectPurgeWorkV1 runs only the canonical fixed UID1000 command owner. A
// root init never runs image Git itself. The actual ctl/session lifetime owns
// the commands; reply/EOF alone never discharges their physical owner.
func (g *Init) inspectPurgeWorkV1(raw json.RawMessage) (proto.PurgeWorkInspectResultV1, error) {
	args, err := decodePurgeWorkV1(raw)
	if err != nil {
		return proto.PurgeWorkInspectResultV1{}, err
	}
	release, err := g.holdCommandOperation()
	if err != nil {
		return proto.PurgeWorkInspectResultV1{}, err
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if g.sess == nil {
		return proto.PurgeWorkInspectResultV1{}, purgeWorkErrorV1("the workspace inspection has no original guest session")
	}
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		select {
		case <-ctx.Done():
		case <-g.sess.Done():
			cancel()
		}
	}()
	defer func() { cancel(); <-joined }()
	result := proto.PurgeWorkInspectResultV1{Version: 1, Primary: args.Primary}
	wanted := map[string]bool{}
	for _, repo := range args.Repositories {
		wanted[repo.Name] = true
	}
	entries, err := os.ReadDir(g.path("/work"))
	if err != nil {
		return result, err
	}
	for _, entry := range entries {
		if !wanted[entry.Name()] {
			result.OutsideRepositories = true
		}
	}
	for _, repo := range args.Repositories {
		value, err := g.inspectPurgeRepositoryV1(ctx, repo)
		if err != nil {
			return proto.PurgeWorkInspectResultV1{}, err
		}
		result.Repositories = append(result.Repositories, value)
	}
	return result, ctx.Err()
}

func (g *Init) inspectPurgeRepositoryV1(ctx context.Context, repo proto.PurgeWorkRepositoryV1) (proto.PurgeWorkRepositoryResultV1, error) {
	result := proto.PurgeWorkRepositoryResultV1{Name: repo.Name}
	cwd := "/work/" + repo.Name
	info, err := os.Lstat(g.path(cwd))
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return result, errors.Join(purgeWorkErrorV1("the protected workspace is missing or is not its original directory"), err)
	}
	gitDirectory, err := os.Lstat(g.path(cwd + "/.git"))
	if os.IsNotExist(err) && repo.Scratch {
		// The actual scratch seed creates a plain directory. Every remaining
		// entry is work; only a physically empty protected directory is clean.
		entries, err := os.ReadDir(g.path(cwd))
		if err != nil {
			return result, err
		}
		result.Dirty = len(entries) != 0
		return result, ctx.Err()
	}
	// Linked worktrees and symlinked repositories need a separate protected
	// source plan. Never follow their .git pointer into another workspace.
	if err != nil || !gitDirectory.IsDir() || gitDirectory.Mode()&os.ModeSymlink != 0 {
		return result, errors.Join(purgeWorkErrorV1("the protected repository is missing or uses an unsupported external Git directory"), err)
	}
	probe := func(arguments ...string) (string, error) {
		out, code, err := g.purgeGitProbeV1(ctx, cwd, arguments...)
		if err != nil || code != 0 {
			return "", errors.Join(purgeWorkErrorV1("the fixed workspace Git inspection did not complete successfully"), err)
		}
		return out, nil
	}
	version, err := probe("--version")
	if err != nil {
		return result, err
	}
	if _, safe := gitpolicy.HookVersion(version); !safe {
		return result, purgeWorkErrorV1("the guest Git cannot disable every configured hook")
	}
	root, err := probe("rev-parse", "--show-toplevel")
	if err != nil || strings.TrimSuffix(root, "\n") != filepath.Clean(g.path(cwd)) {
		return result, errors.Join(purgeWorkErrorV1("the workspace Git selected another repository root"), err)
	}
	status, err := probe("status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return result, err
	}
	result.Dirty = status != ""
	// Include all refs, tags and a detached HEAD, rather than only branches
	// still inside the original session namespace. Renaming a ref cannot hide
	// its unpulled commits. Missing saved objects narrow the exclusions only.
	arguments := []string{"rev-list", "--count", "--all"}
	head, code, err := g.purgeGitProbeV1(ctx, cwd, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	if err != nil || code != 0 && code != 1 {
		return result, errors.Join(purgeWorkErrorV1("the workspace HEAD cannot be inspected"), err)
	}
	if code == 0 {
		oid := strings.TrimSuffix(head, "\n")
		if !api.ValidGitOID(oid) {
			return result, purgeWorkErrorV1("the workspace HEAD is not an exact commit")
		}
		arguments = append(arguments, oid)
	} else {
		// A clean unborn branch has no HEAD commit. A malformed detached HEAD
		// is unknown; it cannot masquerade as an empty repository.
		ref, err := probe("symbolic-ref", "--quiet", "HEAD")
		if err != nil || !strings.HasPrefix(ref, "refs/heads/") {
			return result, errors.Join(purgeWorkErrorV1("the workspace has neither a commit nor a known unborn branch"), err)
		}
		_, missing, err := g.purgeGitProbeV1(ctx, cwd, "show-ref", "--verify", "--quiet", strings.TrimSuffix(ref, "\n"))
		if err != nil || missing != 1 {
			return result, errors.Join(purgeWorkErrorV1("the workspace unborn branch cannot be established"), err)
		}
	}
	arguments = append(arguments, "--not")
	for _, oid := range repo.Saved {
		_, code, err := g.purgeGitProbeV1(ctx, cwd, "cat-file", "-e", oid+"^{commit}")
		if err != nil {
			return result, err
		}
		if code == 0 {
			arguments = append(arguments, oid)
		}
	}
	count, err := probe(arguments...)
	if err != nil {
		return result, err
	}
	text := strings.TrimSuffix(count, "\n")
	result.Unpulled, err = strconv.ParseUint(text, 10, 64)
	if err != nil || text == "" || strconv.FormatUint(result.Unpulled, 10) != text {
		return result, purgeWorkErrorV1("the workspace Git returned an invalid unpulled commit count")
	}
	return result, nil
}

func (g *Init) purgeGitProbeV1(ctx context.Context, cwd string, arguments ...string) (string, int, error) {
	id, err := bundleNonce()
	if err != nil {
		return "", 125, err
	}
	output, diagnostics := &bundleText{remaining: 1 << 20}, &bundleText{remaining: 64 << 10}
	code, err := g.runBundleProbe(ctx, bundleCommand(id, g.path(cwd), arguments...), bundleEnvironment(), output, diagnostics)
	return output.data.String(), code, err
}
