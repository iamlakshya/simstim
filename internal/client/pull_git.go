package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/gitpolicy"
)

const pullMetadataMax = 16 << 20

type pullGit struct {
	root, executable string
	env              []string
	helperEnv        []string
	role             localGitRole
	metadata         *localGitMetadata
	originalCwd      string
	executableFile   *os.File
	executableID     string
	ownership        *localGitOwnership
}

type localGitCheckedSource struct {
	LocalGitSource
	metadata       *localGitMetadata
	executableFile *os.File
	executableID   string
	executablePath string
}

func (s localGitCheckedSource) Revalidate(ctx context.Context) error {
	if err := s.metadata.Revalidate(ctx); err != nil {
		return err
	}
	if err := revalidateGitExecutable(s.executablePath, s.executableID, s.executableFile); err != nil {
		return err
	}
	return s.LocalGitSource.Revalidate(ctx)
}

type pullGitError struct {
	verb       string
	diagnostic string
	cause      error
	code       int
}

func (e *pullGitError) Error() string { return "git " + e.verb + ": " + e.diagnostic }
func (e *pullGitError) Unwrap() error { return e.cause }

type cappedGitOutput struct {
	bytes.Buffer
	limit    int
	exceeded bool
}

func (b *cappedGitOutput) Write(data []byte) (int, error) {
	if b.Len()+len(data) > b.limit {
		b.exceeded = true
		return 0, fmt.Errorf("Git metadata output exceeds its bound")
	}
	return b.Buffer.Write(data)
}

func openPullGit(ctx context.Context, directory string, metadata ...LocalGitMetadataOpener) (_ *pullGit, err error) {
	repo, err := openLocalGit(ctx, directory, metadata...)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, repo.close())
		}
	}()
	top, err := repo.run(ctx, nil, 4096, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, errors.Join(&UserError{Msg: "this folder is not in a Git working repository", Hint: "Choose an ordinary local working clone.", Code: "not_a_repo", Kind: KindFailed}, err)
	}
	repo.root = gitLine(top)
	return repo, nil
}

// openLocalGit supplies the same fixed executable and version gate for
// working clones, static identity and source attribution, including bare repos.
func openLocalGit(ctx context.Context, directory string, supplied ...LocalGitMetadataOpener) (_ *pullGit, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	source, err := openGitMetadataSource(ctx, supplied)
	if err != nil {
		return nil, err
	}
	metadata, err := captureGitMetadata(ctx, source, os.LookupEnv, runtime.GOOS == "windows")
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, metadata.Close())
		}
	}()
	cwd, err := os.Getwd() // PWD was checked before the first metadata lookup
	if err != nil {
		return nil, fmt.Errorf("find the original local directory: %w", err)
	}
	executable, err := metadata.lookupGit(ctx, cwd)
	if err != nil {
		return nil, &UserError{Msg: "Git is not installed on this computer", Hint: "Install Git, then pull again.", Code: "no_git", Kind: KindFailed}
	}
	executableFile, err := os.Open(executable)
	if err != nil {
		return nil, fmt.Errorf("pin the selected native Git executable: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, executableFile.Close())
		}
	}()
	executableID, err := files.Identity(executableFile)
	if err != nil {
		return nil, err
	}
	if directory == "" {
		directory = cwd
	}
	root := directory
	if !filepath.IsAbs(root) {
		root = localInputPath(root, cwd)
	}
	environment, err := metadata.environment(ctx, localGitNormal, "", api.GitIdentity{})
	if err != nil {
		return nil, err
	}
	helperEnvironment, err := metadata.environment(ctx, localGitHelper, "", api.GitIdentity{})
	if err != nil {
		return nil, err
	}
	repo := &pullGit{root: root, executable: executable, env: environment, helperEnv: helperEnvironment, role: localGitNormal,
		metadata: metadata, originalCwd: cwd, executableFile: executableFile, executableID: executableID, ownership: &localGitOwnership{}}
	version, err := repo.run(ctx, nil, 4096, "version")
	if err != nil {
		return nil, err
	}
	if _, safe := gitpolicy.HookVersion(strings.TrimSpace(string(version))); !safe {
		return nil, &UserError{Msg: "this Git version cannot disable every configuration hook safely", Hint: "Use Git2.55 or later, then pull again.", Code: "git_version", Kind: KindFailed}
	}
	return repo, nil
}

func (g *pullGit) close() error {
	err := g.executableFile.Close()
	if errors.Is(err, os.ErrClosed) {
		err = nil
	}
	return errors.Join(g.metadata.Close(), err)
}

func (g *pullGit) run(ctx context.Context, input []byte, limit int, args ...string) ([]byte, error) {
	output := &cappedGitOutput{limit: limit}
	err := g.runIO(ctx, bytes.NewReader(input), output, args...)
	if output.exceeded {
		return nil, errors.Join(pullProtocol("Git metadata was too large to review safely"), err)
	}
	return output.Bytes(), err
}

// runIO is the same canonical local Git executor with a bounded caller sink.
// Native bundle bytes do not pass through a metadata-sized bytes.Buffer.
func (g *pullGit) runIO(ctx context.Context, input io.Reader, output io.Writer, args ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(args) == 0 {
		return &UserError{Code: "git_operation", Msg: "the local Git operation is missing", Hint: "Use a canonical Git recipe.", Kind: KindRefused}
	}
	if g.ownership == nil {
		return &UserError{Code: "git_ownership", Msg: "the local Git operation has no capture owner", Hint: "Open it through the canonical local Git constructor.", Kind: KindRefused}
	}
	if err := g.metadata.Revalidate(ctx); err != nil {
		return err
	}
	if err := g.ownership.quiescent(); err != nil {
		return err
	}
	sourceEnvironment, err := g.metadata.environment(ctx, localGitNormal, "", api.GitIdentity{})
	if err != nil {
		return err
	}
	source, err := openLocalSource(ctx, g.root, args[0] != "version", sourceEnvironment)
	if err != nil {
		return err
	}
	diagnostic := &cappedGitOutput{limit: 64 << 10}
	checked := localGitCheckedSource{LocalGitSource: source, metadata: g.metadata, executableFile: g.executableFile,
		executableID: g.executableID, executablePath: g.executable}
	code, retained, err := runLocalGitOwner(ctx, localGitRequest{Executable: g.executable, ExecutableID: g.executableID, Directory: source.Path(), Args: args,
		Role: g.role, Env: slices.Clone(g.env), HelperEnv: slices.Clone(g.helperEnv), HelperDirectory: g.originalCwd}, input, output, diagnostic, checked)
	if retained != nil {
		retained.source = source
		g.ownership.retain(retained)
		// Do not inspect diagnostics or close source handles while the actual
		// owner is still running. The error itself retains it even if its
		// caller discards this pullGit after the failure.
		return err
	}
	if ctx.Err() != nil {
		return errors.Join(ctx.Err(), err, source.Close())
	}
	if diagnostic.exceeded {
		return errors.Join(pullProtocol("Git diagnostic output was too large to read safely"), err, source.Close())
	}
	if checkErr := source.Revalidate(ctx); checkErr != nil {
		return errors.Join(checkErr, err, source.Close())
	}
	err = errors.Join(err, source.Close())
	if err != nil {
		message := CleanLine(diagnostic.String())
		if message == "" {
			message = CleanLine(err.Error())
		}
		return &pullGitError{verb: args[0], diagnostic: message, cause: err, code: code}
	}
	return nil
}

func gitExit(err error, code int) bool {
	var value *pullGitError
	return errors.As(err, &value) && value.code == code
}

func (g *pullGit) ref(ctx context.Context, name string) (string, error) {
	output, err := g.run(ctx, nil, 4096, "rev-parse", "--verify", "--end-of-options", name+"^{commit}")
	return gitLine(output), err
}

func (g *pullGit) hasCommit(ctx context.Context, oid string) bool {
	_, err := g.run(ctx, nil, 4096, "cat-file", "-e", oid+"^{commit}")
	return err == nil
}

func (g *pullGit) verify(ctx context.Context, bundle string) error {
	_, err := g.run(ctx, nil, 64<<10, "bundle", "verify", bundle)
	return err
}

func (g *pullGit) ancestor(ctx context.Context, base, tip string) (bool, error) {
	_, err := g.run(ctx, nil, 4096, "merge-base", "--is-ancestor", base, tip)
	if gitExit(err, 1) {
		return false, nil
	}
	return err == nil, err
}

var _ io.Writer = (*cappedGitOutput)(nil)

func gitLine(data []byte) string { return strings.TrimSuffix(string(data), "\n") }
