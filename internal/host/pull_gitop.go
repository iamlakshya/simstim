package host

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
)

const gitOpPullBundle = "pull.bundle"

type gitPullArgs struct {
	Dir     string        `json:"dir"`
	ID      string        `json:"id"`
	Output  string        `json:"output"`
	Scratch string        `json:"scratch"`
	Limit   int64         `json:"limit"`
	Heads   []gitPullHead `json:"heads,omitempty"`
}
type gitPullHead struct {
	Name string `json:"name"`
	OID  string `json:"oid"`
}
type gitPullResult struct {
	Path string `json:"path"`
	Ref  string `json:"ref"`
	OID  string `json:"oid"`
}

func init() {
	gitOpKinds = append(gitOpKinds, &gitOpKind{name: gitOpPullBundle, run: func(r gitOpRequest, _ func(*GitInfo)) (gitOpAnswer, error) {
		value, err := captureUnwalledBundle(r.Pull)
		return gitOpAnswer{Pull: value}, err
	}})
}

func captureUnwalledBundle(args *gitPullArgs) (_ *gitPullResult, err error) {
	if args == nil || !api.ValidSessionID(args.ID) || !filepath.IsAbs(args.Dir) || !filepath.IsAbs(args.Output) || filepath.Clean(args.Output) != args.Output || !filepath.IsAbs(args.Scratch) || filepath.Clean(args.Scratch) != args.Scratch || args.Limit <= 0 {
		return nil, UserError("pull_protocol", "invalid scoped bundle request", "Start pull through the current client.")
	}
	executable, err := systemGit()
	if err != nil {
		return nil, err
	}
	policy := context.WithValue(gitContext(), pullGitPolicyKey{}, executable)
	previous := gitCtx.Swap(&policy)
	defer gitCtx.Store(previous)
	if err = gitVersionGate(); err != nil {
		return nil, err
	}
	source, err := repoInfo(args.Dir, false)
	if err != nil {
		return nil, err
	}
	if source.Branch == "HEAD" {
		return nil, UserError("detached_head", "the session has no branch checked out", "Create a branch in the session, then pull again.")
	}
	scratch, err := os.MkdirTemp(args.Output, "capture-")
	if err != nil {
		return nil, fmt.Errorf("make scoped bundle staging: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(scratch)
		}
	}()
	staged := filepath.Join(args.Scratch, "repo.git")
	format, err := git(source.Root, "rev-parse", "--show-object-format")
	if err != nil || (format != "sha1" && format != "sha256") {
		return nil, UserError("pull_format", "the source Git object format is unavailable", "Check the source repository before pulling.")
	}
	if _, err = git(scratch, "init", "--bare", "--template=", "--object-format="+format, staged); err != nil {
		return nil, err
	}
	ref := "refs/heads/" + brand.Name + "/" + args.ID + "/" + Slug(source.Branch)
	oid, err := git(source.Root, "rev-parse", "--verify", "--end-of-options", "HEAD^{commit}")
	if err != nil {
		return nil, err
	}
	if len(args.Heads) > 0 {
		if len(args.Heads) != 1 || args.Heads[0].Name != ref || !api.ValidGitOID(args.Heads[0].OID) {
			return nil, UserError("pull_protocol", "the unwalled retry has an invalid captured branch", "Restart pull to capture the current branch.")
		}
		oid = args.Heads[0].OID
	}
	if !api.ValidGitOID(oid) {
		return nil, UserError("pull_protocol", "the session returned an invalid commit", "Check the session repository before pulling.")
	}
	if _, err = git(staged, "fetch", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head", "--", source.Root, oid+":"+ref); err != nil {
		return nil, err
	}
	path := filepath.Join(scratch, "work.bundle")
	output, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, fmt.Errorf("make scoped bundle file: %w", err)
	}
	defer output.Close()
	limited := &gitBundleWriter{writer: output, limit: args.Limit}
	hooks, err := configHooks(staged)
	if err != nil {
		return nil, err
	}
	command := gitCommand(gitArgs(staged, hooks, "bundle", "create", "-", ref)...)
	diagnostic := &pullDiagnostic{remaining: 64 << 10}
	command.Stdout = limited
	command.Stderr = diagnostic
	if err = command.Run(); err != nil {
		if limited.failure != nil {
			return nil, limited.failure
		}
		return nil, UserError("pull_capture", "scoped Git could not create the session bundle: "+firstLine(diagnostic.String()), "Check the session repository and pull again.")
	}
	if err = output.Sync(); err != nil {
		return nil, fmt.Errorf("sync scoped bundle: %w", err)
	}
	return &gitPullResult{Path: path, Ref: ref, OID: oid}, nil
}

type pullGitPolicyKey struct{}

func pullGitEnvironment() []string {
	var env []string
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if !strings.HasPrefix(key, "GIT_") {
			env = append(env, item)
		}
	}
	return append(env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_SYSTEM="+os.DevNull, "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_ALLOW_PROTOCOL=file", "GIT_TERMINAL_PROMPT=0", "GIT_NO_LAZY_FETCH=1")
}

type gitBundleWriter struct {
	writer       io.Writer
	limit, total int64
	failure      error
}

func (w *gitBundleWriter) Write(data []byte) (int, error) {
	if int64(len(data)) > w.limit-w.total {
		w.failure = UserError("pull_too_big", "the bundle exceeds pull_max", "Reduce the committed work or raise the explicit pull limit.")
		return 0, w.failure
	}
	n, err := w.writer.Write(data)
	w.total += int64(n)
	return n, err
}

type pullDiagnostic struct {
	strings.Builder
	remaining int
}

func (d *pullDiagnostic) Write(data []byte) (int, error) {
	if len(data) > d.remaining {
		return 0, io.ErrShortBuffer
	}
	d.remaining -= len(data)
	return d.Builder.Write(data)
}
