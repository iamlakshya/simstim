package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/proto"
)

type laptopSeed struct {
	declaration api.InputRepository
	directory   string
	bundle      string
	identity    api.GitIdentity
	untracked   []string
	bundleInfo  os.FileInfo
	branch      string
	ownership   *localGitOwnership
}

func (s *laptopSeed) close() error {
	if err := s.ownership.quiescent(); err != nil {
		return err
	}
	return os.RemoveAll(s.directory)
}

// readLaptopGitIdentity intentionally permits static global configuration.
// This read has the canonical no-hook flags but does not inherit GIT_* values.
// It never executes git var, a shell, an editor or a configured command.
func readLaptopGitIdentity(ctx context.Context, git *pullGit) (api.GitIdentity, error) {
	reader := *git
	reader.role = localGitIdentity
	var identity api.GitIdentity
	var err error
	reader.env, err = git.metadata.environment(ctx, localGitIdentity, "", api.GitIdentity{})
	if err != nil {
		return identity, err
	}
	values, err := reader.run(ctx, nil, proto.MaxFrame, "config", "--includes", "--null", "--get-regexp", `^user\.(name|email)$`)
	if err != nil && !gitExit(err, 1) {
		return identity, fmt.Errorf("read static laptop Git identity: %w", err)
	}
	for _, record := range bytes.Split(values, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		key, value, found := bytes.Cut(record, []byte{'\n'})
		if !found || !utf8.Valid(value) || bytes.ContainsAny(value, "\x00\r\n") {
			return identity, &UserError{Code: "seed_identity", Msg: "the laptop Git identity contains invalid text", Hint: "Set ordinary user.name and user.email values in your Git configuration.", Kind: KindUsage}
		}
		switch string(key) {
		case "user.name":
			identity.Name = string(value)
		case "user.email":
			identity.Email = string(value)
		default:
			return identity, copyInputError("seed_identity", "Git returned an unexpected identity field")
		}
	}
	identity.Known = true
	return identity, nil
}

func readCommandGitIdentity(ctx context.Context, originalCwd string, metadata ...LocalGitMetadataOpener) (_ api.GitIdentity, err error) {
	git, err := openLocalGit(ctx, originalCwd, metadata...)
	if err != nil {
		return api.GitIdentity{}, err
	}
	defer func() { err = errors.Join(err, git.close()) }()
	return readLaptopGitIdentity(ctx, git)
}

func createLaptopSeed(ctx context.Context, source string, dirty bool, limit int64, identity api.GitIdentity, base string, metadata ...LocalGitMetadataOpener) (_ *laptopSeed, err error) {
	if limit <= 0 {
		return nil, &UserError{Code: "bad_seed_max", Msg: "seed_max must be a positive size", Hint: "Set sandbox.seed_max to a size such as 512M.", Kind: KindUsage}
	}
	git, err := openPullGit(ctx, source, metadata...)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, git.close()) }()
	name := filepath.Base(git.root)
	if !api.InputRepoName(name) {
		return nil, &UserError{Code: "seed_name", Msg: "the laptop repository has no usable basename", Hint: "Clone it into a folder with an ordinary visible name.", Kind: KindUsage}
	}
	if !identity.Known {
		return nil, &UserError{Code: "no_git", Msg: "the laptop Git identity could not be read", Hint: "Install Git on this computer before creating a clone.", Kind: KindFailed}
	}
	directory, err := os.MkdirTemp("", "laptop-seed-")
	if err != nil {
		return nil, err
	}
	seed := &laptopSeed{directory: directory, bundle: filepath.Join(directory, "seed.bundle"), identity: identity, ownership: git.ownership}
	defer func() {
		if err != nil {
			err = errors.Join(err, seed.close())
		}
	}()
	if base == "" {
		base = "HEAD"
	}
	commit, err := git.ref(ctx, base)
	if err != nil || !api.ValidGitOID(commit) {
		return nil, &UserError{Code: "seed_history", Msg: "the laptop repository has no committed HEAD", Hint: "Create an initial commit before seeding it.", Kind: KindUsage}
	}
	branch, err := git.run(ctx, nil, 4096, "rev-parse", "--symbolic-full-name", "--verify", "--end-of-options", base)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(gitLine(branch), "refs/heads/") {
		branch = []byte("refs/heads/laptop-seed\n")
	}
	seed.branch = gitLine(branch)
	origin, err := git.run(ctx, nil, 64<<10, "config", "--get", "remote.origin.url")
	if gitExit(err, 1) {
		origin, err = nil, nil
	}
	if err != nil {
		return nil, err
	}
	if dirty {
		commit, seed.untracked, err = dirtySeedCommit(ctx, git, directory, commit, identity)
		if err != nil {
			return nil, err
		}
	}
	objects, err := git.run(ctx, nil, 4096, "rev-parse", "--path-format=absolute", "--git-path", "objects")
	if err != nil {
		return nil, err
	}
	objectRoot := gitLine(objects)
	if !filepath.IsAbs(objectRoot) || strings.ContainsAny(objectRoot, "\x00\r\n") {
		return nil, &UserError{Code: "seed_objects", Msg: "the laptop object store cannot be represented safely", Hint: "Use an ordinary clone with a clean absolute object-store path.", Kind: KindRefused}
	}
	staging := filepath.Join(directory, "repo.git")
	stage := *git
	stage.root = directory
	format := "sha1"
	if len(commit) == 64 {
		format = "sha256"
	}
	if _, err = stage.run(ctx, nil, 64<<10, "init", "--bare", "--template=", "--object-format="+format, staging); err != nil {
		return nil, err
	}
	if err = os.WriteFile(filepath.Join(staging, "objects", "info", "alternates"), []byte(objectRoot+"\n"), 0600); err != nil {
		return nil, err
	}
	stage.root = staging
	sourceRef := gitLine(branch)
	if !strings.HasPrefix(sourceRef, "refs/heads/") {
		return nil, copyInputError("seed_branch", "the laptop HEAD does not name a branch")
	}
	if _, err = stage.run(ctx, nil, 4096, "check-ref-format", sourceRef); err != nil {
		return nil, err
	}
	if _, err = stage.run(ctx, nil, 4096, "update-ref", sourceRef, commit); err != nil {
		return nil, err
	}
	output, err := os.OpenFile(seed.bundle, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	sum := sha256.New()
	bounded := &seedBoundedWriter{target: io.MultiWriter(output, sum), remaining: limit}
	err = stage.runIO(ctx, nil, bounded, "bundle", "create", "-", sourceRef)
	if err == nil {
		err = output.Sync()
	}
	if err == nil {
		seed.bundleInfo, err = output.Stat()
	}
	closeErr := output.Close()
	if bounded.exceeded {
		return nil, &UserError{Code: "seed_too_big", Msg: "the laptop bundle exceeds seed_max", Hint: "Clone the repository on the host, or raise sandbox.seed_max before trying again.", Kind: KindRefused}
	}
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	seed.declaration = api.InputRepository{Name: name, Commit: commit, SourceBranch: sourceRef, Origin: gitLine(origin), Size: limit - bounded.remaining, SHA256: hex.EncodeToString(sum.Sum(nil)), Dirty: dirty, ObjectFormat: format}
	// The checked-out branch is display context only. The bundle has exactly
	// the one selected source ref, so unrelated branches and tags cannot leak.
	return seed, nil
}

func (s *laptopSeed) write(ctx context.Context, target io.Writer, limit int64) error {
	if s.declaration.Size > limit || limit <= 0 {
		return copyInputError("seed_too_big", "the sealed bundle exceeds the current seed_max")
	}
	root, err := os.OpenRoot(s.directory)
	if err != nil {
		return err
	}
	defer root.Close()
	f, err := files.OpenContainedRegular(root, "seed.bundle")
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !os.SameFile(st, s.bundleInfo) || st.Size() != s.declaration.Size {
		return copyInputError("seed_changed", "the sealed laptop bundle changed before streaming")
	}
	sum := sha256.New()
	n, err := io.Copy(io.MultiWriter(target, sum), &inputContextReader{ctx: ctx, reader: io.LimitReader(f, s.declaration.Size)})
	if err != nil {
		return err
	}
	var extra [1]byte
	more, endErr := f.Read(extra[:])
	if n != s.declaration.Size || more != 0 || endErr != io.EOF || hex.EncodeToString(sum.Sum(nil)) != s.declaration.SHA256 {
		return copyInputError("seed_changed", "the laptop bundle changed while streaming")
	}
	return nil
}

type seedBoundedWriter struct {
	target    io.Writer
	remaining int64
	exceeded  bool
}

func (w *seedBoundedWriter) Write(data []byte) (int, error) {
	if int64(len(data)) > w.remaining {
		w.exceeded = true
		return 0, fmt.Errorf("laptop seed exceeds its byte limit")
	}
	n, err := w.target.Write(data)
	w.remaining -= int64(n)
	return n, err
}
