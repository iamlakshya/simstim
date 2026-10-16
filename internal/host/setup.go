package host

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/paths"
)

// Repository setup hooks (roadmap N6). A repository can keep an executable
// .<name>/setup, which runs in a new session's terminal before the agent, and
// a .<name>/resume, which runs before a respawn. A repository can be
// untrusted, so its hooks run only when the user said yes to that repository
// on this host; the client asks and keeps the answers. The host's own hooks,
// in ~/.config/<name>/hooks/, are the user's, and run when the repository
// has none or is not trusted.

// sessionHooks are the hooks a new session runs.
type sessionHooks struct {
	setup, resume string
}

// setupHooks picks a new session's hooks for the folder cwd. When the
// repository has hooks and neither a.Setup nor the user's answers decide, it
// returns a setup_trust error that asks the user, after removing the
// worktree gi that this start made.
func setupHooks(a NewArgs, cwd string, gi *GitInfo) (sessionHooks, error) {
	repo, top := repoOf(cwd)
	repoSetup, repoResume := hookIn(top, "setup"), hookIn(top, "resume")
	run, decided := setupDecision(a, repo)
	if !decided && (repoSetup != "" || repoResume != "") {
		if gi != nil {
			discardWorktree(gi)
		}
		script := repoSetup
		if script == "" {
			script = repoResume
		}
		return sessionHooks{}, setupQuestion(repo, script)
	}
	var h sessionHooks
	if run {
		h.setup, h.resume = repoSetup, repoResume
	}
	if h.setup == "" {
		h.setup = hostHook("setup")
	}
	if h.resume == "" {
		h.resume = hostHook("resume")
	}
	return h, nil
}

// setupDecision says whether the repository's hooks may run, and whether
// anything decided it: the request's override, or the user's answer.
func setupDecision(a NewArgs, repo string) (run, decided bool) {
	switch a.Setup {
	case api.SetupRun:
		return true, true
	case api.SetupSkip:
		return false, true
	}
	run, decided = a.SetupTrust[repo]
	return run, decided
}

// setupQuestion asks the user whether the setup script of repo may run.
func setupQuestion(repo, script string) error {
	return questionError(api.CodeSetupTrust, fmt.Sprintf("%s has a setup script, %s, and you have not said whether it may run on this host", repo, script),
		"Pass --setup to run it, or --no-setup to start without it. The launcher asks.", api.SetupQuestion{Repo: repo, Script: script})
}

// repoOf finds the repository of dir: its main folder, which every worktree
// of it shares and which keys the user's answer, and dir's own top level,
// where its hooks are. Outside git, both are dir.
func repoOf(dir string) (repo, top string) {
	top, err := git(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return dir, dir
	}
	common, err := git(dir, "rev-parse", "--git-common-dir")
	if err != nil {
		return top, top
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(dir, common)
	}
	common = filepath.Clean(common)
	if filepath.Base(common) == ".git" {
		return filepath.Dir(common), top
	}
	return common, top // a bare repository
}

// hookIn is dir's executable .<name>/<kind>, or "".
func hookIn(dir, kind string) string { return executable(filepath.Join(dir, "."+brand.Name, kind)) }

// hostHook is the host's own executable hook of this kind, or "".
func hostHook(kind string) string { return executable(filepath.Join(paths.ConfigDir(), "hooks", kind)) }

// executable is p when it is an executable file, or "".
func executable(p string) string {
	if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() && st.Mode()&0o111 != 0 {
		return p
	}
	return ""
}

// discardWorktree removes a worktree this start just made, and its branch.
func discardWorktree(g *GitInfo) {
	_, _ = git(g.RepoRoot, "worktree", "unlock", g.Worktree)
	_, _ = git(g.RepoRoot, "worktree", "remove", "--force", g.Worktree)
	_, _ = git(g.RepoRoot, "branch", "-D", g.Branch)
}

// hookVars tell a hook about its session: its name and folder, whether its
// worktree is new, the main repository of a worktree, and which hook runs.
func hookVars(m *Manifest, restored bool) []string {
	kind, fresh := "setup", "0"
	if restored {
		kind = "resume"
	}
	if m.Git != nil && m.Git.CreatedBy && !restored {
		fresh = "1"
	}
	vars := []string{
		brand.Env("HOOK") + "=" + kind,
		brand.Env("SESSION_NAME") + "=" + m.Name,
		brand.Env("SESSION_DIR") + "=" + m.Cwd,
		brand.Env("WORKTREE_NEW") + "=" + fresh,
	}
	if m.Git != nil {
		vars = append(vars, brand.Env("REPO")+"="+m.Git.RepoRoot)
	}
	return vars
}

// quoteVars writes NAME=value pairs for a shell command line.
func quoteVars(vars []string) string {
	q := make([]string, len(vars))
	for i, kv := range vars {
		k, v, _ := strings.Cut(kv, "=")
		q[i] = k + "=" + shellQuote(v)
	}
	return strings.Join(q, " ")
}
