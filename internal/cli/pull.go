package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/client/review"
)

func pullCmd() *cobra.Command {
	var into string
	var force, allowFlagged bool
	cmd := &cobra.Command{
		Use:   "pull <host>/<session>",
		Short: "Bring a session's branch into the git repository you are in",
		Long: "Fetches the branch that the session's folder has checked out, as a git bundle verified into a quarantine ref,\n" +
			"reviews flagged files, and updates a branch of the current repository: the same name, or --into.\n" +
			"It changes neither working tree and works over any host transport.",
		Example: "  " + brand.Name + " pull my-vps/auth-fix\n  " + brand.Name + " pull my-vps/auth-fix --into review/auth-fix",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := gitHere(cmd.Context(), "rev-parse", "--show-toplevel"); err != nil {
				return &client.UserError{Msg: "this folder is not in a git repository", Hint: "Run pull again in your clone of the repository.", Code: "not_a_repo"}
			}
			return withSession(cmd, args[0], func(ctx context.Context, h client.Host, conn *client.Conn, s *client.Session) error {
				if conn.HasFeature(api.FeaturePullBundle) {
					return pullBundle(cmd, ctx, h, s, into, force, allowFlagged)
				}
				return pull(ctx, h, s, into, force)
			})
		},
	}
	cmd.Flags().StringVar(&into, "into", "", "the local branch to fetch into (default: the session's branch name)")
	cmd.Flags().BoolVar(&force, "force", false, "move the local branch even when the fetch is not a fast-forward")
	cmd.Flags().BoolVar(&allowFlagged, "allow-flagged", false, "update the branch even when the diff touches flagged files")
	return cmd
}

func pullBundle(cmd *cobra.Command, ctx context.Context, h client.Host, s *client.Session, into string, force, allowFlagged bool) error {
	p := prompts(cmd)
	confirm := func(res review.Result) (bool, error) {
		if !p.on {
			return false, nil
		}
		fmt.Fprintf(os.Stderr, "Flagged changes: %s\n", client.CleanLine(res.Summary()))
		return p.yesNo("Update the local branch with these flagged changes?", false)
	}
	res, err := client.PullBundle(ctx, h, s, client.PullBundleOptions{
		LocalRepo:      ".",
		Into:           into,
		Force:          force,
		AllowFlagged:   allowFlagged,
		ConfirmFlagged: confirm,
	})
	if err != nil {
		return err
	}
	if res.NothingNew {
		fmt.Fprintf(os.Stderr, "Nothing new on %s from %s on %s.\n", client.CleanLine(res.Branch), client.CleanLine(s.Name), h.Name)
		if res.Dirty > 0 {
			fmt.Fprintf(os.Stderr, "The session has %d files with uncommitted changes; they stay on the host until they are committed there.\n", res.Dirty)
		}
		return nil
	}
	fmt.Fprintf(os.Stderr, "Fetched %s from %s on %s into %s", client.CleanLine(res.Branch), client.CleanLine(s.Name), h.Name, client.CleanLine(res.Into))
	if res.Ahead != "" && res.Ahead != "0" {
		fmt.Fprintf(os.Stderr, ": %s commits that your current branch does not have", res.Ahead)
	}
	fmt.Fprintln(os.Stderr, ".")
	if res.Dirty > 0 {
		fmt.Fprintf(os.Stderr, "The session has %d files with uncommitted changes; they stay on the host until they are committed there.\n", res.Dirty)
	}
	return nil
}

func pull(ctx context.Context, h client.Host, s *client.Session, into string, force bool) error {
	if h.SSH == "" {
		return &client.UserError{Msg: fmt.Sprintf("pull needs SSH, and %s is reached through %s", h.Name, h.Target()), Hint: "Push the branch from the session to a remote you share, then fetch it here.", Code: "needs_ssh"}
	}
	repo, err := client.SessionRepo(ctx, h, s.Cwd)
	if err != nil {
		return err
	}
	if repo.Branch == "HEAD" {
		return &client.UserError{Msg: client.CleanLine(s.Name) + " has no branch checked out (a detached HEAD)", Hint: "Create a branch in the session, then pull again.", Code: "detached_head"}
	}
	if into == "" {
		into = repo.Branch
	}
	if cur, _ := gitOut(ctx, "symbolic-ref", "--quiet", "--short", "HEAD"); cur == into {
		return &client.UserError{Msg: client.CleanLine(into) + " is the branch you have checked out here", Hint: "Pull into another branch with --into, or switch branches first.", Code: "branch_checked_out"}
	}
	sshCmd, err := client.GitSSHCommand(h)
	if err != nil {
		return err
	}
	git := exec.CommandContext(ctx, "git", client.FetchArgs(h, repo.Root, repo.Branch, into, force)...)
	git.Env = append(os.Environ(), "GIT_SSH_COMMAND="+sshCmd)
	out, err := git.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if strings.Contains(msg, "non-fast-forward") || strings.Contains(msg, "rejected") {
			return &client.UserError{Msg: fmt.Sprintf("%s here has commits that %s on %s does not", client.CleanLine(into), client.CleanLine(repo.Branch), h.Name), Hint: "Pull into another branch with --into, or replace it with --force.", Code: "branch_diverged"}
		}
		return &client.UserError{Msg: "git fetch failed: " + client.CleanLine(msg), Hint: "Check that ssh " + h.SSH + " works, then run the git fetch yourself.", Code: "fetch_failed"}
	}
	ahead, _ := gitOut(ctx, "rev-list", "--count", into, "^HEAD")
	fmt.Fprintf(os.Stderr, "Fetched %s from %s on %s into %s", shown(repo.Branch), shown(nameOf(*s)), shown(h.Name), shown(into))
	if ahead != "" && ahead != "0" {
		fmt.Fprintf(os.Stderr, ": %s commits that your current branch does not have", ahead)
	}
	fmt.Fprintln(os.Stderr, ".")
	if repo.Dirty > 0 {
		fmt.Fprintf(os.Stderr, "The session has %d files with uncommitted changes; they stay on the host until they are committed there.\n", repo.Dirty)
	}
	return nil
}

func gitHere(ctx context.Context, args ...string) error {
	_, err := gitOut(ctx, args...)
	return err
}

func gitOut(ctx context.Context, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", args...).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", fmt.Errorf("git %s: %s", args[0], strings.TrimSpace(string(ee.Stderr)))
		}
		return "", fmt.Errorf("git %s: %w", args[0], err)
	}
	return strings.TrimSpace(string(out)), nil
}
