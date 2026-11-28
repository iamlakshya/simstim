package guest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/link"
)

const bundleTimeout = 2 * time.Minute

// serveBundle packs the session's current branch as a git bundle strictly
// inside the sandbox over a link.KindBundle stream (isolation spec 6.10, 7.1).
// The host never runs git inside the sandbox-writable workspace.
func (r *runner) serveBundle(ctx context.Context, st *link.Stream) {
	defer st.Close()
	var hdr BundleHeader
	if err := st.Header(&hdr); err != nil {
		writeBundleErr(st, "bad_request", "bundle stream had an invalid header", "Update the host helper.")
		return
	}
	if hdr.Dir != "" && hdr.Dir != "out" {
		writeBundleErr(st, "bad_request", "unsupported bundle direction "+hdr.Dir, "Update the host helper.")
		return
	}
	if hdr.Base != "" && !isValidGitOID(hdr.Base) {
		writeBundleErr(st, "bad_request", "invalid base commit ID", "Update the client.")
		return
	}

	workDir, env := r.bundleContext(hdr)
	if _, err := exec.LookPath("git"); err != nil {
		writeBundleErr(st, "no_git", "git is not installed in the sandbox", "Install git in the sandbox image or on the host.")
		return
	}

	bctx, cancel := context.WithTimeout(ctx, bundleTimeout)
	defer cancel()
	stop := context.AfterFunc(ctx, func() {
		cancel()
		_ = st.Close()
	})
	defer stop()

	repo, head, errCode, errMsg, errHint := r.inspectGuestRepo(bctx, workDir, env)
	if errCode != "" {
		writeBundleErr(st, errCode, errMsg, errHint)
		return
	}

	maxBytes := hdr.MaxBytes
	if maxBytes <= 0 {
		maxBytes = api.DefaultPullMaxBytes
	}

	full, empty := r.resolveBundleMode(bctx, repo.Root, env, "refs/heads/"+repo.Branch, hdr.Base, head, hdr.Full)
	if empty {
		writeBundleHeader(st, api.PullBundleHeader{
			Repo:  repo,
			Head:  head,
			Empty: true,
		})
		return
	}

	r.packAndStreamBundle(bctx, st, repo, env, hdr.Base, head, full, maxBytes)
}

func (r *runner) bundleContext(hdr BundleHeader) (workDir string, env []string) {
	sessDir, sessEnv := r.sessionContext(hdr.Session)
	workDir = hdr.WorkDir
	if workDir == "" {
		workDir = sessDir
	}
	if workDir == "" {
		workDir, _ = os.Getwd()
	}
	if len(sessEnv) > 0 {
		env = sessEnv
	} else {
		env = os.Environ()
	}
	env = withEnvVar(env, "GIT_TERMINAL_PROMPT", "0")
	return workDir, env
}

func (r *runner) inspectGuestRepo(ctx context.Context, workDir string, env []string) (repo *api.RepoInfo, head, code, msg, hint string) {
	root, err := r.runManagedGit(ctx, workDir, env, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, "", "not_a_repo", workDir + " is not in a git repository", "Start the session in a repository or a worktree to pull its work."
	}
	branch, err := r.runManagedGit(ctx, root, env, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return nil, "", "git", err.Error(), "Check the repository in the session with git status."
	}
	if branch == "HEAD" {
		return nil, "", "detached_head", "the session has no branch checked out (a detached HEAD)", "Create a branch in the session, then pull again."
	}
	if _, err := r.runManagedGit(ctx, root, env, "check-ref-format", "--branch", branch); err != nil {
		return nil, "", "bad_request", "invalid branch name", "Check the branch in the session."
	}
	head, err = r.runManagedGit(ctx, root, env, "rev-parse", "--verify", "refs/heads/"+branch)
	if err != nil {
		return nil, "", "git", err.Error(), "Check the branch in the session."
	}
	dirty := 0
	if statusOut, serr := r.runManagedGit(ctx, root, env, "status", "--porcelain"); serr == nil && statusOut != "" {
		for _, line := range strings.Split(statusOut, "\n") {
			if strings.TrimSpace(line) != "" {
				dirty++
			}
		}
	}
	return &api.RepoInfo{
		Root:   root,
		Main:   root,
		Branch: branch,
		Dirty:  dirty,
	}, head, "", "", ""
}

func (r *runner) resolveBundleMode(ctx context.Context, root string, env []string, ref, base, head string, reqFull bool) (full, empty bool) {
	full = reqFull || base == ""
	if full {
		return true, false
	}
	if base == head {
		return false, true
	}
	if _, err := r.runManagedGit(ctx, root, env, "merge-base", "--is-ancestor", base, ref); err == nil {
		if cnt, cerr := r.runManagedGit(ctx, root, env, "rev-list", "--count", base+".."+ref); cerr == nil && strings.TrimSpace(cnt) == "0" {
			return false, true
		}
		return false, false
	}
	return true, false
}

func (r *runner) packAndStreamBundle(ctx context.Context, st *link.Stream, repo *api.RepoInfo, env []string, base, head string, full bool, maxBytes int64) {
	f, err := os.CreateTemp(r.runDir, "pull-*.bundle")
	if err != nil {
		writeBundleErr(st, "write", "could not create bundle file: "+err.Error(), "Check disk space in the sandbox.")
		return
	}
	tmpPath := f.Name()
	_ = f.Close()
	defer os.Remove(tmpPath)

	args := []string{"bundle", "create", tmpPath, "refs/heads/" + repo.Branch}
	if !full {
		args = append(args, "^"+base)
	}
	if _, err := r.runManagedGit(ctx, repo.Root, env, args...); err != nil {
		writeBundleErr(st, "git", err.Error(), "Check the repository in the session with git status.")
		return
	}
	info, err := os.Stat(tmpPath)
	if err != nil {
		writeBundleErr(st, "read", "could not stat bundle file: "+err.Error(), "Try pulling again.")
		return
	}
	if info.Size() > maxBytes {
		writeBundleErr(st, "pull_too_big",
			fmt.Sprintf("the git bundle is %s, above the %s limit", formatBundleBytes(info.Size()), formatBundleBytes(maxBytes)),
			"Push the branch to a shared remote, or raise sandbox.pull_max in config.toml.")
		return
	}
	bf, err := os.Open(tmpPath)
	if err != nil {
		writeBundleErr(st, "read", "could not open bundle file: "+err.Error(), "Try pulling again.")
		return
	}
	defer bf.Close()

	if !writeBundleHeader(st, api.PullBundleHeader{
		Repo: repo,
		Head: head,
		Full: full,
		Size: info.Size(),
	}) {
		return
	}
	_, _ = io.Copy(st, io.LimitReader(bf, info.Size()))
}

// runManagedGit runs git inside the sandbox with hooks and fsmonitor disabled,
// registering its PID in r.managed so the PID 1 orphan reaper never races with
// cmd.Wait.
func (r *runner) runManagedGit(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	full := append([]string{
		"-c", "core.fsmonitor=false",
		"-c", "core.hooksPath=/dev/null",
		"-c", "safe.directory=*",
		"-C", dir,
	}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.WaitDelay = 2 * time.Second

	r.mu.Lock()
	err := cmd.Start()
	var pid int
	if err == nil && cmd.Process != nil {
		pid = cmd.Process.Pid
		r.managed[pid] = struct{}{}
	}
	r.mu.Unlock()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", args[0], err)
	}
	waitErr := cmd.Wait()
	if pid > 0 {
		r.mu.Lock()
		delete(r.managed, pid)
		r.mu.Unlock()
	}
	if waitErr != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = waitErr.Error()
		}
		return "", errors.New("git " + args[0] + ": " + msg)
	}
	return strings.TrimSpace(stdout.String()), nil
}

func writeBundleHeader(w io.Writer, hdr api.PullBundleHeader) bool {
	b, err := json.Marshal(hdr)
	if err != nil {
		return false
	}
	_, err = w.Write(append(b, '\n'))
	return err == nil
}

func writeBundleErr(w io.Writer, code, msg, hint string) {
	writeBundleHeader(w, api.PullBundleHeader{
		Error: &struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Hint    string `json:"hint,omitempty"`
		}{Code: code, Message: msg, Hint: hint},
	})
}

func isValidGitOID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

func formatBundleBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	default:
		return strconv.FormatInt(n, 10) + " bytes"
	}
}
