package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/proto"
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
// folder is in, on the host: its top folder (Root), its branch, or "HEAD"
// when detached, and the count of files with uncommitted changes (Dirty).
type RepoInfo = api.RepoInfo

// SessionRepo asks the host about the git repository at dir. The helper
// runs git there in its git operation repo.info: with the repository's hooks
// and fsmonitor off, and in a scope under the sessions' slice when the host
// has a systemd user manager.
func SessionRepo(ctx context.Context, h Host, dir string) (RepoInfo, error) {
	s := &SSH{Host: h}
	req, err := json.Marshal(api.RepoInfoArgs{Dir: dir, Changes: true})
	if err != nil {
		return RepoInfo{}, fmt.Errorf("write the request for the repository: %w", err)
	}
	out, err := s.Run(ctx, RemoteHelper(HelperVersion())+" git-op "+api.GitOpRepoInfo, bytes.NewReader(req))
	if err != nil {
		return RepoInfo{}, &UserError{Msg: fmt.Sprintf("could not ask %s about the repository at %s: %s", h.Name, CleanPath(dir), CleanLine(err.Error())),
			Hint: brand.Name + " host doctor " + h.Name}
	}
	return parseRepoInfo(h, dir, out)
}

// parseRepoInfo reads the answer of repo.info about dir on h. Each text
// that it prints from the host passes the cleaner (hostError, CleanLine,
// CleanPath): a filter of the repository writes what git says.
func parseRepoInfo(h Host, dir string, out []byte) (RepoInfo, error) {
	var ans struct {
		Error *proto.Error  `json:"error"`
		Repo  *api.RepoInfo `json:"repo"`
	}
	if err := json.Unmarshal(out, &ans); err != nil || (ans.Error == nil && ans.Repo == nil) {
		// The host's output shows escaped to ASCII: it is the host's text.
		return RepoInfo{}, &UserError{Msg: fmt.Sprintf("could not read what %s said about the repository at %s: %+q", h.Name, CleanPath(dir), CleanLine(strings.TrimSpace(string(out)))),
			Hint: brand.Name + " host add " + h.Name + "   (reinstalls the helper)"}
	}
	switch e := ans.Error; {
	case e == nil:
		return *ans.Repo, nil
	case e.Code == "no_git":
		return RepoInfo{}, &UserError{Msg: "git is not installed on " + h.Name, Hint: "install git on " + h.Name + ", then pull again", Code: e.Code}
	case e.Code == "not_a_repo":
		return RepoInfo{}, &UserError{Msg: fmt.Sprintf("%s on %s is not in a git repository", CleanPath(dir), h.Name), Hint: "pull works for sessions started in a repository or a worktree", Code: e.Code}
	default:
		ue := hostErrorOn(h.Name, e)
		if ue.Hint == "" {
			ue.Hint = "check the repository with git status on " + h.Name
		}
		return RepoInfo{}, ue
	}
}

// hostErrorOn is hostError, cleaned, for an answer of host. The helper's
// git errors say "on this host", and the client names the host instead. A
// git_scope error's hint runs the host doctor for that host.
func hostErrorOn(host string, e *proto.Error) *UserError {
	ue := hostError(e)
	switch e.Code {
	case api.CodeGitScope:
		ue.Hint = brand.Name + " host doctor " + CleanLine(host)
		fallthrough
	case api.CodeGitHooks, api.CodeGitTimeout, api.CodeGitVersion:
		ue.Msg = strings.Replace(ue.Msg, "on this host", "on "+CleanLine(host), 1)
	}
	return ue
}

// WorktreeNote is what a delete on host says about the session's worktree,
// and what to try when an error kept it, cleaned and formatted as
// hostErrorOn does.
func WorktreeNote(host string, res DeleteResult) (note, hint string) {
	if res.WorktreeCode == "" {
		return CleanPath(res.Worktree), ""
	}
	ue := hostErrorOn(host, &proto.Error{Code: res.WorktreeCode, Message: res.Worktree, Hint: res.WorktreeHint})
	return ue.Msg, ue.Hint
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
