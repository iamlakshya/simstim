package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

func pullCmd() *cobra.Command {
	var into string
	var force bool
	cmd := &cobra.Command{
		Use:   "pull <host>/<session>",
		Short: "Bring a session's branch into the git repository you are in",
		Long: "Fetches the branch that the session's folder has checked out, over your SSH connection, into a branch\n" +
			"of the repository in the current folder: the same name, or --into. It changes neither working tree.",
		Example: "  " + brand.Name + " pull my-vps/auth-fix\n  " + brand.Name + " pull my-vps/auth-fix --into review/auth-fix",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := gitHere(cmd.Context(), "rev-parse", "--show-toplevel"); err != nil {
				return &client.UserError{Msg: "this folder is not in a git repository", Hint: "cd into your clone of the repository, then run pull again"}
			}
			return withSession(cmd.Context(), args[0], func(ctx context.Context, h client.Host, _ *client.Conn, s *client.Session) error {
				return pull(ctx, h, s, into, force)
			})
		},
	}
	cmd.Flags().StringVar(&into, "into", "", "the local branch to fetch into (default: the session's branch name)")
	cmd.Flags().BoolVar(&force, "force", false, "move the local branch even when the fetch is not a fast-forward")
	return cmd
}

func pull(ctx context.Context, h client.Host, s *client.Session, into string, force bool) error {
	if h.SSH == "" {
		return &client.UserError{Msg: fmt.Sprintf("pull needs SSH, and %s is reached through %s", h.Name, h.Target()), Hint: "Push the branch from the session to a remote you share, then fetch it here."}
	}
	repo, err := client.SessionRepo(ctx, h, s.Cwd)
	if err != nil {
		return err
	}
	if repo.Branch == "HEAD" {
		return &client.UserError{Msg: client.CleanLine(s.Name) + " has no branch checked out (a detached HEAD)", Hint: "create a branch in the session, then pull again"}
	}
	if into == "" {
		into = repo.Branch
	}
	if cur, _ := gitOut(ctx, "symbolic-ref", "--quiet", "--short", "HEAD"); cur == into {
		return &client.UserError{Msg: client.CleanLine(into) + " is the branch you have checked out here", Hint: "pull into another branch with --into, or switch branches first"}
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
			return &client.UserError{Msg: fmt.Sprintf("%s here has commits that %s on %s does not", client.CleanLine(into), client.CleanLine(repo.Branch), h.Name),
				Hint: "pull into another branch with --into, or replace it with --force"}
		}
		return &client.UserError{Msg: "git fetch failed: " + client.CleanLine(msg), Hint: "ssh " + h.SSH + " works? Then try git fetch yourself"}
	}
	ahead, _ := gitOut(ctx, "rev-list", "--count", into, "^HEAD")
	fmt.Printf("Fetched %s from %s on %s into %s", client.CleanLine(repo.Branch), client.CleanLine(s.Name), h.Name, client.CleanLine(into))
	if ahead != "" && ahead != "0" {
		fmt.Printf(": %s commits that your current branch does not have", ahead)
	}
	fmt.Println(".")
	if repo.Dirty > 0 {
		fmt.Printf("The session has %d files with uncommitted changes; they stay on the host until they are committed there.\n", repo.Dirty)
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
