package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"unicode"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client/review"
	"github.com/iamlakshya/simstim/internal/proto"
)

// DefaultPullMaxBytes caps a git bundle fetched by pull (512 MiB, spec 6.10).
const DefaultPullMaxBytes int64 = 512 << 20

// PullBundleOptions controls PullBundle.
type PullBundleOptions struct {
	LocalRepo      string
	Into           string
	Force          bool
	AllowFlagged   bool
	MaxBytes       int64
	ConfirmFlagged func(res review.Result) (bool, error)
}

// PullBundleResult reports what PullBundle fetched and updated.
type PullBundleResult struct {
	Branch        string
	Into          string
	Head          string
	QuarantineRef string
	Dirty         int
	Ahead         string
	NothingNew    bool
	Review        review.Result
}

// PullBundle brings session s's checked-out branch on host h into the local git
// repository using a git bundle streamed over the host's Transport, verified
// with `git bundle verify`, fetched into a quarantine ref with fsck enabled and
// no tags, reviewed against flags.toml, and only then applied to `Into`
// (spec 6.10).
func PullBundle(ctx context.Context, h Host, s *Session, opt PullBundleOptions) (PullBundleResult, error) {
	t, err := HostTransport(ctx, h, true)
	if err != nil {
		return PullBundleResult{}, err
	}
	return PullBundleOn(ctx, t, h, s, opt)
}

// PullBundleOn runs PullBundle over an explicit Transport (used directly by
// tests and by PullBundle).
func PullBundleOn(ctx context.Context, t Transport, h Host, s *Session, opt PullBundleOptions) (PullBundleResult, error) {
	if opt.MaxBytes <= 0 {
		opt.MaxBytes = DefaultPullMaxBytes
	}
	if opt.LocalRepo == "" {
		opt.LocalRepo = "."
	}
	base, _ := localGitOut(ctx, opt.LocalRepo, "rev-parse", "--verify", "HEAD")

	hdr, tmpPath, err := fetchBundleStream(ctx, t, h, s, api.PullBundleArgs{
		Base:     base,
		MaxBytes: opt.MaxBytes,
	})
	if err != nil {
		return PullBundleResult{}, err
	}
	defer func() {
		if tmpPath != "" {
			_ = os.Remove(tmpPath)
		}
	}()
	into, err := validatePullHeader(ctx, opt, h, &hdr)
	if err != nil {
		return PullBundleResult{}, err
	}
	if hdr.Empty {
		return emptyPullResult(hdr, into), nil
	}

	hdr, tmpPath, err = verifyOrRetryBundle(ctx, t, h, s, opt, hdr, tmpPath)
	if err != nil {
		return PullBundleResult{}, err
	}
	into, err = validatePullHeader(ctx, opt, h, &hdr)
	if err != nil {
		return PullBundleResult{}, err
	}
	if hdr.Empty {
		return emptyPullResult(hdr, into), nil
	}

	qRef := fmt.Sprintf("refs/%s/sessions/%s/%s/%s", brand.Name, h.Name, s.ID, hdr.Repo.Branch)
	refspec := "+refs/heads/" + hdr.Repo.Branch + ":" + qRef
	if err := localGitRun(ctx, opt.LocalRepo,
		"-c", "transfer.fsckObjects=true",
		"-c", "fetch.fsckObjects=true",
		"fetch", "--no-tags", tmpPath, refspec,
	); err != nil {
		return PullBundleResult{}, &UserError{
			Msg:  "git fetch from bundle failed: " + CleanLine(err.Error()),
			Hint: "Check the repository on " + h.Name + ".",
			Code: "fetch_failed",
		}
	}

	rev, err := reviewQuarantinedBundle(ctx, opt, base, qRef)
	if err != nil {
		return PullBundleResult{}, err
	}
	qSHA, ahead, err := applyPulledBranch(ctx, opt, h, hdr.Repo.Branch, into, qRef)
	if err != nil {
		return PullBundleResult{}, err
	}
	return PullBundleResult{
		Branch:        hdr.Repo.Branch,
		Into:          into,
		Head:          qSHA,
		QuarantineRef: qRef,
		Dirty:         hdr.Repo.Dirty,
		Ahead:         ahead,
		Review:        rev,
	}, nil
}

func emptyPullResult(hdr api.PullBundleHeader, into string) PullBundleResult {
	return PullBundleResult{
		Branch:     hdr.Repo.Branch,
		Into:       into,
		Head:       hdr.Head,
		Dirty:      hdr.Repo.Dirty,
		NothingNew: true,
	}
}

func validatePullHeader(ctx context.Context, opt PullBundleOptions, h Host, hdr *api.PullBundleHeader) (string, error) {
	if hdr.Repo == nil {
		return "", &UserError{
			Msg:  "could not read repository details from " + h.Name,
			Hint: brand.Name + " host add " + h.Name + " (updates the helper)",
			Code: "protocol",
		}
	}
	if err := validateGitBranchName(ctx, opt.LocalRepo, hdr.Repo.Branch, "protocol", "Check the branch name on "+h.Name+"."); err != nil {
		return "", err
	}
	into := opt.Into
	if into == "" {
		into = hdr.Repo.Branch
	} else if err := validateGitBranchName(ctx, opt.LocalRepo, into, "bad_branch", "Pass a valid git branch name with --into."); err != nil {
		return "", err
	}
	if cur, _ := localGitOut(ctx, opt.LocalRepo, "symbolic-ref", "--quiet", "--short", "HEAD"); cur == into {
		return "", &UserError{
			Msg:  CleanLine(into) + " is the branch you have checked out here",
			Hint: "Pull into another branch with --into, or switch branches first.",
			Code: "branch_checked_out",
		}
	}
	return into, nil
}

func validateGitBranchName(ctx context.Context, repoDir, name, code, hint string) error {
	if name == "" || strings.HasPrefix(name, "-") || strings.ContainsAny(name, ":\x00") || strings.ContainsFunc(name, unicode.IsControl) {
		return &UserError{
			Msg:  fmt.Sprintf("invalid branch name %q", CleanLine(name)),
			Hint: hint,
			Code: code,
			Kind: KindUsage,
		}
	}
	if err := localGitRun(ctx, repoDir, "check-ref-format", "--branch", name); err != nil {
		return &UserError{
			Msg:  fmt.Sprintf("invalid branch name %q", CleanLine(name)),
			Hint: hint,
			Code: code,
			Kind: KindUsage,
		}
	}
	return nil
}

func verifyOrRetryBundle(ctx context.Context, t Transport, h Host, s *Session, opt PullBundleOptions, hdr api.PullBundleHeader, tmpPath string) (api.PullBundleHeader, string, error) {
	if err := localGitRun(ctx, opt.LocalRepo, "bundle", "verify", tmpPath); err == nil {
		return hdr, tmpPath, nil
	} else if hdr.Full {
		return hdr, tmpPath, &UserError{
			Msg:  "git bundle verify failed: " + CleanLine(err.Error()),
			Hint: "Run the pull again, or inspect the repository on " + h.Name + ".",
			Code: "bundle_invalid",
		}
	}
	_ = os.Remove(tmpPath)
	hdr, newPath, err := fetchBundleStream(ctx, t, h, s, api.PullBundleArgs{
		Full:     true,
		MaxBytes: opt.MaxBytes,
	})
	if err != nil {
		return api.PullBundleHeader{}, "", err
	}
	if hdr.Repo == nil || hdr.Empty {
		return hdr, newPath, nil
	}
	if newPath == "" {
		return hdr, "", &UserError{
			Msg:  "git bundle verify failed: empty bundle path",
			Hint: "Run the pull again, or inspect the repository on " + h.Name + ".",
			Code: "bundle_invalid",
		}
	}
	if err := localGitRun(ctx, opt.LocalRepo, "bundle", "verify", newPath); err != nil {
		return hdr, newPath, &UserError{
			Msg:  "git bundle verify failed: " + CleanLine(err.Error()),
			Hint: "Run the pull again, or inspect the repository on " + h.Name + ".",
			Code: "bundle_invalid",
		}
	}
	return hdr, newPath, nil
}

func reviewQuarantinedBundle(ctx context.Context, opt PullBundleOptions, base, qRef string) (review.Result, error) {
	reviewBase := ""
	if base != "" {
		if mb, err := localGitOut(ctx, opt.LocalRepo, "merge-base", base, qRef); err == nil && mb != "" {
			reviewBase = mb
		}
	}
	rev, err := review.ReviewGitDiff(ctx, opt.LocalRepo, reviewBase, qRef)
	if err != nil {
		return review.Result{}, fmt.Errorf("review pulled changes: %w", err)
	}
	if hard, ok := rev.HardBlocked(); ok {
		return review.Result{}, &UserError{
			Msg:  fmt.Sprintf("refused %s: %s", CleanPath(hard.Path), hard.Detail),
			Hint: "Fix the file in the session before pulling.",
			Code: "pull_flagged",
			Kind: KindRefused,
		}
	}
	if rev.Flagged() && !opt.AllowFlagged {
		approved, cerr := confirmPullFlagged(rev, opt)
		if cerr != nil {
			return review.Result{}, cerr
		}
		if !approved {
			return review.Result{}, &UserError{
				Msg:  "the pull holds flagged files: " + CleanLine(rev.Summary()),
				Hint: fmt.Sprintf("Inspect %s with git diff, then run %s pull again with --allow-flagged.", qRef, brand.Name),
				Code: "pull_flagged",
				Kind: KindRefused,
			}
		}
	}
	return rev, nil
}

func confirmPullFlagged(rev review.Result, opt PullBundleOptions) (bool, error) {
	if opt.ConfirmFlagged == nil {
		return false, nil
	}
	return opt.ConfirmFlagged(rev)
}

func applyPulledBranch(ctx context.Context, opt PullBundleOptions, h Host, remoteBranch, into, qRef string) (qSHA, ahead string, err error) {
	targetRef := "refs/heads/" + into
	if oldSHA, err := localGitOut(ctx, opt.LocalRepo, "rev-parse", "--verify", targetRef); err == nil && oldSHA != "" && !opt.Force {
		if err := localGitRun(ctx, opt.LocalRepo, "merge-base", "--is-ancestor", oldSHA, qRef); err != nil {
			return "", "", &UserError{
				Msg:  fmt.Sprintf("%s here has commits that %s on %s does not", CleanLine(into), CleanLine(remoteBranch), h.Name),
				Hint: "Pull into another branch with --into, or replace it with --force.",
				Code: "branch_diverged",
			}
		}
	}
	qSHA, err = localGitOut(ctx, opt.LocalRepo, "rev-parse", "--verify", qRef)
	if err != nil {
		return "", "", fmt.Errorf("resolve %s: %w", qRef, err)
	}
	if err := localGitRun(ctx, opt.LocalRepo, "update-ref", targetRef, qSHA); err != nil {
		return "", "", &UserError{
			Msg:  "could not update local branch " + CleanLine(into) + ": " + CleanLine(err.Error()),
			Hint: "Pull into another branch with --into.",
			Code: "fetch_failed",
		}
	}
	ahead, _ = localGitOut(ctx, opt.LocalRepo, "rev-list", "--count", targetRef, "^HEAD", "--")
	return qSHA, ahead, nil
}

func fetchBundleStream(ctx context.Context, t Transport, h Host, s *Session, args api.PullBundleArgs) (api.PullBundleHeader, string, error) {
	remote := RemoteHelper(HelperVersion()) + " pull-bundle " + shellWord(s.ID)
	proc, err := t.Start(ctx, remote)
	if err != nil {
		return api.PullBundleHeader{}, "", err
	}
	defer func() {
		proc.Kill()
		_ = proc.Wait()
	}()

	reqBytes, _ := json.Marshal(args)
	if _, err := proc.Stdin().Write(append(reqBytes, '\n')); err != nil {
		return api.PullBundleHeader{}, "", fmt.Errorf("send pull-bundle request: %w", err)
	}
	_ = proc.Stdin().Close()

	br := bufio.NewReaderSize(proc.Stdout(), 64<<10)
	line, err := br.ReadBytes('\n')
	if err != nil {
		msg := tailStderr(proc.Stderr())
		if msg != "" {
			return api.PullBundleHeader{}, "", &UserError{
				Msg:  fmt.Sprintf("could not pull bundle from %s: %s", h.Name, CleanLine(msg)),
				Hint: brand.Name + " host doctor " + h.Name,
				Code: "ssh_failed",
			}
		}
		return api.PullBundleHeader{}, "", fmt.Errorf("read pull-bundle header: %w", err)
	}
	var hdr api.PullBundleHeader
	if err := json.Unmarshal(line, &hdr); err != nil {
		return api.PullBundleHeader{}, "", &UserError{
			Msg:  fmt.Sprintf("could not read pull answer from %s: %+q", h.Name, CleanLine(strings.TrimSpace(string(line)))),
			Hint: brand.Name + " host add " + h.Name + " (updates the helper)",
			Code: "protocol",
		}
	}
	if hdr.Error != nil {
		return api.PullBundleHeader{}, "", pullBundleHostError(h, s.Cwd, hdr.Error.Code, hdr.Error.Message, hdr.Error.Hint)
	}
	if hdr.Empty {
		return hdr, "", nil
	}
	if hdr.Size < 0 || hdr.Size > args.MaxBytes {
		return api.PullBundleHeader{}, "", &UserError{
			Msg:  fmt.Sprintf("the git bundle from %s is %s, above the %s limit", h.Name, formatCopySize(hdr.Size), formatCopySize(args.MaxBytes)),
			Hint: "Push the branch to a shared remote instead.",
			Code: "pull_too_big",
			Kind: KindRefused,
		}
	}
	tmp, err := os.CreateTemp("", brand.Name+"-pull-*.bundle")
	if err != nil {
		return api.PullBundleHeader{}, "", fmt.Errorf("create temp bundle: %w", err)
	}
	tmpPath := tmp.Name()
	n, err := io.Copy(tmp, io.LimitReader(br, args.MaxBytes+1))
	cerr := tmp.Close()
	if n > args.MaxBytes {
		_ = os.Remove(tmpPath)
		return api.PullBundleHeader{}, "", &UserError{
			Msg:  fmt.Sprintf("the git bundle from %s exceeded the %s limit", h.Name, formatCopySize(args.MaxBytes)),
			Hint: "Push the branch to a shared remote instead.",
			Code: "pull_too_big",
			Kind: KindRefused,
		}
	}
	if err != nil || cerr != nil {
		_ = os.Remove(tmpPath)
		if err == nil {
			err = cerr
		}
		return api.PullBundleHeader{}, "", fmt.Errorf("save bundle: %w", err)
	}
	return hdr, tmpPath, nil
}

func pullBundleHostError(h Host, dir, code, msg, hint string) error {
	pe := &proto.Error{Code: code, Message: msg, Hint: hint}
	switch code {
	case "no_git":
		return &UserError{Msg: "git is not installed on " + h.Name, Hint: "Install git on " + h.Name + ", then pull again.", Code: code}
	case "not_a_repo":
		if dir != "" {
			msg = fmt.Sprintf("%s on %s is not in a git repository", CleanPath(dir), h.Name)
		} else {
			msg = CleanLine(msg)
		}
		return &UserError{Msg: msg, Hint: "Start the session in a repository or a worktree to pull its work.", Code: code}
	case "detached_head":
		return &UserError{Msg: CleanLine(msg), Hint: "Create a branch in the session, then pull again.", Code: code}
	default:
		ue := hostErrorOn(h.Name, pe)
		if ue.Hint == "" {
			ue.Hint = "Check the repository with git status on " + h.Name + "."
		}
		return ue
	}
}

func localGitOut(ctx context.Context, dir string, args ...string) (string, error) {
	full := append([]string{"-C", dir, "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false"}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", errors.New(msg)
	}
	return strings.TrimSpace(string(out)), nil
}

func localGitRun(ctx context.Context, dir string, args ...string) error {
	_, err := localGitOut(ctx, dir, args...)
	return err
}
