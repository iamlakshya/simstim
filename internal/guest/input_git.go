package guest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/inputtransfer"
	"github.com/iamlakshya/simstim/internal/proto"
)

func (g *Init) cloneInputRepository(ctx context.Context, stage *inputStage, session string, index int, repository api.InputRepository, identity api.GitIdentity, seedMax int64) (returned error) {
	if !identity.Known {
		return inputError("the laptop clone has no known static Git identity result")
	}
	file, err := files.OpenRegular(stage.payload, inputtransfer.BundleName(index))
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, file.Close()) }()
	snapshot, err := sealInputBundle(ctx, file, repository, seedMax)
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, snapshot.Close()) }()
	work := g.path("/work")
	root, err := os.OpenRoot(work)
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, root.Close()) }()
	// Init creates the fresh destination; fixed-account Git can populate it.
	// Neither a bundle nor a helper receipt chooses an arbitrary directory.
	if err = root.Mkdir(repository.Name, 0o700); err != nil {
		return err
	}
	if err = root.Chown(repository.Name, AgentUID, AgentGID); err != nil {
		return err
	}
	destination := filepath.Join(work, repository.Name)
	if _, err = g.inputGit(ctx, snapshot, work, "clone", "--no-checkout", "--no-hardlinks", "--template=", "--", "/proc/self/fd/3", destination); err != nil {
		return err
	}
	head, err := g.inputGit(ctx, nil, destination, "rev-parse", "--show-object-format")
	if err != nil || strings.TrimSpace(string(head)) != repository.ObjectFormat {
		return inputError("the cloned repository object format differs from the protected source")
	}
	if repository.SourceBranch != "" {
		if !strings.HasPrefix(repository.SourceBranch, "refs/heads/") {
			return inputError("the protected seed source is not an exact branch ref")
		}
		head, err = g.inputGit(ctx, nil, destination, "rev-parse", "--verify", "--end-of-options", repository.SourceBranch+"^{commit}")
		if err != nil || strings.TrimSpace(string(head)) != repository.Commit {
			return inputError("the cloned source head differs from the authenticated client head")
		}
	}
	branch := brand.Name + "/" + session + "/" + host.Slug(repository.Name)
	if _, err = g.inputGit(ctx, nil, destination, "checkout", "--no-track", "-b", branch, repository.Commit); err != nil {
		return err
	}
	head, err = g.inputGit(ctx, nil, destination, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || strings.TrimSpace(string(head)) != repository.Commit {
		return inputError("the checked-out input head differs from the authenticated client head")
	}
	if repository.Origin != "" {
		_, err = g.inputGit(ctx, nil, destination, "config", "--local", "--replace-all", "--", "remote.origin.url", repository.Origin)
	} else {
		_, err = g.inputGit(ctx, nil, destination, "remote", "remove", "origin")
	}
	if err != nil {
		return err
	}
	for _, pair := range [][2]string{{"user.name", identity.Name}, {"user.email", identity.Email}} {
		if pair[1] != "" {
			if _, err = g.inputGit(ctx, nil, destination, "config", "--local", "--", pair[0], pair[1]); err != nil {
				return err
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Release this exact staged bundle before cloning the next repository.
	// The sealed descriptor, not this mutable pathname, was the Git input.
	protected, err := file.Stat()
	if err != nil {
		return err
	}
	current, err := stage.payload.Lstat(inputtransfer.BundleName(index))
	if err != nil || !os.SameFile(protected, current) {
		return inputError("the staged seed identity changed before release")
	}
	return stage.payload.Remove(inputtransfer.BundleName(index))
}

func (g *Init) inputGit(ctx context.Context, bundle *os.File, dir string, args ...string) ([]byte, error) {
	argv := append([]string{"/usr/bin/git"}, host.SafeGitArgs(dir, args...)...)
	run := newOwnedCommand(ctx, proto.CtlExecArgs{Argv: argv, Cwd: dir}, inputCommandEnv())
	run.vmRole, run.inputBundle = vmSeedCommand, bundle
	defer run.cancel()
	var output inputGitOutput
	if code := g.executeOwned(run, ownedCommandIO{Out: &output, Err: io.Discard}); code != 0 {
		return nil, inputError("the fixed-account native input Git command failed")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return bytes.Clone(output.Bytes()), nil
}

type inputGitOutput struct{ bytes.Buffer }

func (w *inputGitOutput) Write(data []byte) (int, error) {
	if len(data) > (64<<10)-w.Len() {
		return 0, inputError("native input Git metadata exceeds its bound")
	}
	return w.Buffer.Write(data)
}
