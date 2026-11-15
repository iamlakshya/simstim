package host

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/gitpolicy"
	"github.com/iamlakshya/simstim/internal/jail"
	"github.com/iamlakshya/simstim/internal/paths"
)

var seedJailCommand = jail.Command

var seedCommitRE = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

func createSeed(a NewArgs, id, name, cwd string) (_ Seed, err error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ended, err := liveGitOp("seed", cancel)
	if err != nil {
		return Seed{}, err
	}
	defer ended()
	check := func() error {
		if err := ctx.Err(); err != nil {
			return seedContextError(err)
		}
		return seedConnectionAlive()
	}
	if err = check(); err != nil {
		return Seed{}, err
	}

	root, metadata, err := seedRoot(cwd)
	if err != nil {
		return Seed{}, asUserError(err, "seed_source", "Use a trusted repository with local Git metadata as the source.")
	}
	if root == "" {
		if a.Base != "" || len(a.Include) > 0 {
			return Seed{}, UserError("not_a_repo", "a scratch workspace has no repository to seed or include", "Start in a Git repository, or omit --base and --include.")
		}
		return Seed{Repo: "scratch", Scratch: true}, nil
	}
	basename := filepath.Base(root)
	if !api.InputRepoName(basename) {
		return Seed{}, UserError("seed_plan", "the source repository has no usable literal basename", "Choose a source directory with a valid basename.")
	}
	session := paths.SessionDir(id)
	if err = paths.EnsurePrivateDir(session); err != nil {
		return Seed{}, err
	}
	scratch, err := os.MkdirTemp(session, "seed-build-")
	if err != nil {
		return Seed{}, err
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err = errors.Join(err, jail.RemoveAll(ctx, scratch))
	}()
	run := func(dir string, args ...string) (string, error) {
		if err := check(); err != nil {
			return "", err
		}
		return seedGit(root, metadata, scratch, dir, args...)
	}
	version, versionErr := run(scratch, "version")
	if versionErr != nil {
		return Seed{}, versionErr
	}
	if err := seedVersion(version); err != nil {
		return Seed{}, err
	}
	base := a.Base
	if base == "" {
		base = "HEAD"
	}
	commit, err := run(root, "rev-parse", "--verify", "--end-of-options", base+"^{commit}")
	if err != nil {
		return Seed{}, err
	}
	if !seedCommitRE.MatchString(commit) {
		return Seed{}, UserError("bad_base", "the seed base is not a commit", "Pass an existing branch or commit as --base.")
	}
	origin, err := run(root, "config", "--get", "remote.origin.url")
	if err != nil {
		return Seed{}, err
	}
	origin = safeSeedOrigin(origin)
	selectedRef, err := run(root, "rev-parse", "--symbolic-full-name", "--verify", "--end-of-options", base)
	if err != nil {
		return Seed{}, err
	}
	if !utf8.ValidString(selectedRef) {
		return Seed{}, UserError("seed_git", "the selected Git branch name is not UTF-8", "Use a UTF-8 branch name, or select its commit with --base.")
	}
	if !strings.HasPrefix(selectedRef, "refs/heads/") {
		selectedRef = "refs/heads/seed"
	}
	staging := filepath.Join(scratch, "repo.git")
	if _, err = run(scratch, "init", "--bare", "--template=", staging); err != nil {
		return Seed{}, err
	}
	// Fetch only this ancestry into a fresh trusted staging repository. A raw
	// commit can then be bundled under one named ref, without exporting any
	// other branch, stash, config or untracked source file.
	if _, err = run(staging, "fetch", "--no-tags", "--no-write-fetch-head", "--", root, commit+":"+selectedRef); err != nil {
		return Seed{}, err
	}
	bundle := filepath.Join(scratch, "seed.bundle")
	if _, err = run(staging, "bundle", "create", bundle, selectedRef); err != nil {
		return Seed{}, err
	}
	src, err := os.OpenRoot(scratch)
	if err != nil {
		return Seed{}, err
	}
	defer src.Close()
	dst, err := os.OpenRoot(session)
	if err != nil {
		return Seed{}, err
	}
	defer dst.Close()
	if err = check(); err != nil {
		return Seed{}, err
	}
	input, err := files.OpenRegular(src, "seed.bundle")
	if err != nil {
		return Seed{}, err
	}
	defer input.Close()
	output, err := dst.OpenFile("seed.bundle.new", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return Seed{}, err
	}
	size, err := seedCopy(ctx, output, input)
	if err == nil {
		err = output.Sync()
	}
	closeErr := output.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return Seed{}, seedContextError(err)
		}
		return Seed{}, err
	}
	if err = check(); err != nil {
		return Seed{}, err
	}
	if err = dst.Rename("seed.bundle.new", "seed.bundle"); err != nil {
		return Seed{}, err
	}
	seed := Seed{Repo: basename, Commit: commit, Branch: brand.Name + "/" + id + "/" + Slug(name), Origin: origin, BundleSize: size, SourceBranch: strings.TrimPrefix(selectedRef, "refs/heads/")}
	if err = check(); err != nil {
		return Seed{}, err
	}
	if err = dst.Mkdir("seed-includes", 0700); err != nil {
		return Seed{}, err
	}
	included, err := dst.OpenRoot("seed-includes")
	if err != nil {
		return Seed{}, err
	}
	defer included.Close()
	source, err := os.OpenRoot(root)
	if err != nil {
		return Seed{}, err
	}
	defer source.Close()
	for _, entry := range slices.Compact(slices.Sorted(slices.Values(a.Include))) {
		if err = check(); err != nil {
			return Seed{}, err
		}
		if !files.Relative(entry) {
			return Seed{}, UserError("bad_include", "an include must be a path beneath the repository", "Pass a relative regular file such as --include .env.")
		}
		f, e := files.OpenIncluded(source, entry)
		if e != nil {
			return Seed{}, asUserError(e, "bad_include", "Use a regular file that stays within the source repository.")
		}
		if err = included.MkdirAll(filepath.Dir(entry), 0700); err != nil {
			f.Close()
			return Seed{}, err
		}
		out, e := included.OpenFile(entry, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			f.Close()
			return Seed{}, e
		}
		n, e := seedCopy(ctx, out, f)
		f.Close()
		if e == nil {
			e = out.Sync()
		}
		closeErr := out.Close()
		if e == nil {
			e = closeErr
		}
		if e != nil {
			if errors.Is(e, context.Canceled) || errors.Is(e, context.DeadlineExceeded) {
				return Seed{}, seedContextError(e)
			}
			return Seed{}, e
		}
		if err = check(); err != nil {
			return Seed{}, err
		}
		seed.Includes = append(seed.Includes, SeedFile{Path: entry, Size: n})
	}
	if err = check(); err != nil {
		return Seed{}, err
	}
	return seed, nil
}

func seedGit(root string, metadata []string, scratch, dir string, args ...string) (string, error) {
	if err := seedConnectionAlive(); err != nil {
		return "", err
	}
	if err := trustedSource(root); err != nil {
		return "", err
	}
	bin, err := systemGit()
	if err != nil {
		return "", err
	}
	binds := []jail.Bind{{Path: scratch, Write: true}}
	add := func(p string) {
		for _, b := range binds {
			if p == b.Path || strings.HasPrefix(p, b.Path+"/") {
				return
			}
		}
		binds = append(binds, jail.Bind{Path: p})
	}
	for _, p := range []string{"/usr", "/bin", "/sbin", "/lib", "/lib64", "/etc/alternatives", "/etc/ld.so.cache", "/dev/null"} {
		if _, e := os.Stat(p); e == nil {
			add(p)
		}
	}
	for _, p := range metadata {
		add(p)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	ended, err := liveGitOp("seed", cancel)
	if err != nil {
		return "", err
	}
	defer ended()
	argv := append([]string{bin}, SafeGitArgs(dir, args...)...)
	// Existing source repositories can have 0700 folders and 0600 Git
	// metadata. The login identity is required to read them. This caller
	// requests it explicitly, without a failed subuid attempt or any change
	// to source permissions. The canonical namespace jail still confines it.
	cmd, err := seedJailCommand(ctx, jail.Spec{Argv: argv, Env: []string{"PATH=/usr/bin:/bin", "HOME=" + scratch, "XDG_CONFIG_HOME=" + scratch, "XDG_CACHE_HOME=" + scratch, "XDG_STATE_HOME=" + scratch, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "LANG=C", "LC_ALL=C"}, Binds: binds, Subuid: false, Net: jail.NetNone, Seccomp: jail.ProfileGit})
	if err != nil {
		return "", err
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return "", seedContextError(ctx.Err())
		}
		if cmd.ExitCode() == 1 && slices.Equal(args, []string{"config", "--get", "remote.origin.url"}) {
			return "", nil
		}
		return "", UserError("seed_git", "Git could not make the seed: "+firstLine(string(out)), "Use a self-contained source clone with local Git metadata. If the jail cannot start, run host prepare.")
	}
	return strings.TrimSpace(string(out)), nil
}

func seedVersion(version string) error {
	matched, safe := gitpolicy.HookVersion(version)
	if matched == "" || !safe {
		return UserError("git_version", "the system Git version cannot disable every repository hook safely", "Install Git 2.55 or later through the host package manager.")
	}
	return nil
}

func seedContextError(cause error) error {
	code, message, hint := "cancelled", "the seed stopped because its connection ended", "Start the session again."
	if errors.Is(cause, context.DeadlineExceeded) {
		code, message, hint = "git_timeout", "Git ran past the seed's deadline", "Check the source repository and the host's free space, then try again."
	}
	return fmt.Errorf("%w: %w", UserError(code, message, hint), cause)
}
