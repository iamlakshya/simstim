package client

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// GitSSHCommand is the ssh command line for GIT_SSH_COMMAND, so git reaches
// a host the way the client does: through the client's own SSH config and
// shared connection, with no forwarding.
func GitSSHCommand(h Host) (string, error) {
	s := &SSH{Host: h}
	args, err := s.baseArgs()
	if err != nil {
		return "", err
	}
	parts := []string{"ssh"}
	for _, a := range append(args, NoPortForwards()...) {
		parts = append(parts, shellWord(a))
	}
	return strings.Join(parts, " "), nil
}

// shellWord quotes s for a POSIX shell when it needs it.
func shellWord(s string) string {
	if s != "" && !strings.ContainsAny(s, " '\"\\$`!*?[]{}()<>|&;#~=%") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// RepoInfo is what pull needs to know about the git repository a session's
// folder is in, on the host.
type RepoInfo struct {
	Root   string // the repository's top folder
	Branch string // the branch checked out there, or "HEAD" when detached
	Dirty  int    // files with uncommitted changes
}

// SessionRepo asks the host about the git repository at dir.
func SessionRepo(ctx context.Context, h Host, dir string) (RepoInfo, error) {
	s := &SSH{Host: h}
	cmd := fmt.Sprintf(`cd %s && git rev-parse --show-toplevel && git rev-parse --abbrev-ref HEAD && git status --porcelain | wc -l`, shellWord(dir))
	out, err := s.Run(ctx, cmd, nil)
	if err != nil {
		return RepoInfo{}, &UserError{Msg: fmt.Sprintf("%s on %s is not in a git repository", CleanPath(dir), h.Name), Hint: "pull works for sessions started in a repository or a worktree"}
	}
	return parseRepoInfo(string(out))
}

func parseRepoInfo(out string) (RepoInfo, error) {
	// The host's output shows escaped to ASCII: it is the host's text.
	bad := &UserError{Msg: fmt.Sprintf("could not read the repository from %+q", CleanLine(out)),
		Hint: "check that git works in the session's folder on the host, then pull again"}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 3 {
		return RepoInfo{}, bad
	}
	dirty, err := strconv.Atoi(strings.TrimSpace(lines[2]))
	if err != nil {
		return RepoInfo{}, bad
	}
	return RepoInfo{Root: strings.TrimSpace(lines[0]), Branch: strings.TrimSpace(lines[1]), Dirty: dirty}, nil
}

// FetchArgs is the git fetch that brings branch from the host's repository
// at root into the local branch into. force allows a fetch that is not a
// fast-forward.
func FetchArgs(h Host, root, branch, into string, force bool) []string {
	spec := "refs/heads/" + branch + ":refs/heads/" + into
	if force {
		spec = "+" + spec
	}
	return []string{"fetch", "--no-tags", h.SSH + ":" + root, spec}
}
