package host

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/paths"
)

// Repository setup hooks (roadmap N6). A repository can keep an executable
// .<name>/setup, which runs in a new session's terminal before the agent, and
// a .<name>/resume, which runs before a respawn. A repository can be
// untrusted, so its hooks run only when the user said yes to their content
// on this host: the client keeps, for each yes, the setup hash of the
// scripts that it was for, and a change asks again (spec 19.2). The host's
// own hooks, in ~/.config/<name>/hooks/, are the user's, and run when the
// repository has none or is not trusted.

// sessionHooks are the hooks a new session runs, and the setup hash of the
// repository's scripts when they are among them. stop is the repository's
// .<name>/stop: the hash covers it, but no session runs it yet (spec 17.11).
type sessionHooks struct {
	setup, resume, stop string
	hash                string
}

// any says whether the repository has a script.
func (s sessionHooks) any() bool { return s.setup != "" || s.resume != "" || s.stop != "" }

// readScripts reads a repository's scripts. Tests replace it, to see that a
// decision reads none.
var readScripts = setupHash

// setupHooks picks a new session's hooks for the folder cwd. When the
// repository has hooks and neither a.Setup nor the user's answers decide, it
// returns a setup_trust error that asks the user. When a hook cannot run, it
// returns a setup_outside or setup_unreadable error. Each error first
// removes the worktree gi that this start made.
func setupHooks(a NewArgs, cwd string, gi *GitInfo) (sessionHooks, error) {
	repo, top := repoOf(cwd)
	h, err := repoHooks(a, repo, top)
	if err != nil {
		if gi != nil {
			discardWorktree(gi)
		}
		return sessionHooks{}, err
	}
	if h.setup == "" {
		h.setup = hostHook("setup")
	}
	if h.resume == "" {
		h.resume = hostHook("resume")
	}
	return h, nil
}

// repoHooks returns the repository's scripts that a new session runs, by
// the first rule of spec 19.2 that matches: the request's override, the
// user's refusal, then the user's yes for the scripts' setup hash. --no-setup
// and a refusal decide before the host reads any script, so they also win
// over a script that cannot run. Otherwise it returns a question for the
// user.
func repoHooks(a NewArgs, repo, top string) (sessionHooks, error) {
	run, known := a.SetupTrust[repo]
	if a.Setup == api.SetupSkip || a.Setup != api.SetupRun && known && !run {
		return sessionHooks{}, nil
	}
	s, err := readScripts(top)
	switch {
	case err != nil:
		return sessionHooks{}, scriptUserError(repo, err)
	case !s.any():
		return sessionHooks{}, nil
	case a.Setup == api.SetupRun, a.SetupHashes[repo] == s.hash:
		return s, nil
	}
	return sessionHooks{}, setupQuestion(repo, s, a.SetupHashes[repo] != "")
}

// setupQuestion asks the user whether the scripts s of repo may run. changed
// says that the user trusted other content of them.
func setupQuestion(repo string, s sessionHooks, changed bool) error {
	script := s.setup
	if script == "" {
		script = s.resume
	}
	if script == "" {
		script = s.stop
	}
	msg := fmt.Sprintf("%s has a setup script, %s, and you have not said whether it may run on this host", repo, script)
	if changed {
		msg = fmt.Sprintf("the setup script of %s changed after you trusted it", repo)
	}
	return questionError(api.CodeSetupTrust, msg, "Pass --setup to run it, or --no-setup to start without it. The launcher asks.",
		api.SetupQuestion{Repo: repo, Script: script, Hash: s.hash})
}

// scriptError is a repository's script that cannot run: one that leaves the
// repository through a symlink, or one that cannot be read. With no script,
// it is the top level itself, which cannot be opened.
type scriptError struct {
	script  string // its path in the repository, such as .<name>/setup
	top     string // the top level, when it cannot be opened
	outside bool   // it leaves the repository
	err     error  // why it cannot be read, when it is inside
}

func (e *scriptError) Error() string {
	switch {
	case e.outside:
		return e.script + " points outside the repository"
	case e.script == "":
		return "could not read the folder " + e.top + ": " + reason(e.err).Error()
	}
	return "could not read " + e.script + ": " + reason(e.err).Error()
}

func (e *scriptError) Unwrap() error { return e.err }

// reason is err without the operation and the path that a file system error
// names, such as "permission denied".
func reason(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

// scriptUserError is the setup_outside or setup_unreadable error of repo for
// setupHash's error.
func scriptUserError(repo string, err error) error {
	var e *scriptError
	switch {
	case !errors.As(err, &e):
		return err
	case e.outside:
		return UserError(api.CodeSetupOutside,
			fmt.Sprintf("the setup script of %s, %s, points outside the repository, so it cannot run", repo, e.script),
			fmt.Sprintf("Make %s a file in the repository, or start with --no-setup.", e.script))
	case e.script == "":
		return UserError(api.CodeSetupUnreadable,
			fmt.Sprintf("could not read the folder %s, so the setup scripts of %s cannot be checked: %v", e.top, repo, reason(e.err)),
			fmt.Sprintf("Let your user on the host read the folder %s, or start with --no-setup.", e.top))
	}
	return UserError(api.CodeSetupUnreadable,
		fmt.Sprintf("could not read the setup script of %s, %s: %v", repo, e.script, reason(e.err)),
		fmt.Sprintf("Let your user on the host read %s, or start with --no-setup.", e.script))
}

// setupHash reads the setup, resume and stop scripts at a repository's top
// level through an os.Root there. It returns their paths and their setup
// hash: the SHA-256 of "v2\nsetup <hex>\nresume <hex>\nstop <hex>\n", where
// each <hex> is the SHA-256 of a script's bytes, or "none" for a script
// that is absent (spec 19.2). A script that is not an executable regular
// file is absent. So is a symlink out of top to nothing, or to a file that
// is not executable, since neither could run. A script that leaves top
// through a symlink to an executable file, or through any absolute symlink,
// which the root refuses, is a *scriptError that is outside. An executable
// script that cannot be read is a *scriptError too, and so is a top level
// that cannot be opened.
func setupHash(top string) (sessionHooks, error) {
	var s sessionHooks
	root, err := os.OpenRoot(top)
	if err != nil {
		// A folder that can be searched but not read, such as one of mode
		// 0111, cannot be opened, but a script in it can still run by its
		// path. So what it holds is unknown, which is not "no scripts".
		return sessionHooks{}, &scriptError{top: top, err: err}
	}
	defer root.Close()
	var text strings.Builder
	text.WriteString("v2\n")
	for _, kind := range []string{"setup", "resume", "stop"} {
		rel := filepath.Join("."+brand.Name, kind)
		path := filepath.Join(top, rel)
		sum, err := scriptSum(root, rel)
		var unreadable *scriptError
		switch {
		case errors.As(err, &unreadable):
			return sessionHooks{}, unreadable
		case err != nil && executable(path) != "":
			// The root refused a script that the plain path finds.
			return sessionHooks{}, &scriptError{script: rel, outside: true}
		case err != nil || sum == "":
			sum = "none"
		case kind == "setup":
			s.setup = path
		case kind == "resume":
			s.resume = path
		default:
			s.stop = path
		}
		text.WriteString(kind + " " + sum + "\n")
	}
	h := sha256.Sum256([]byte(text.String()))
	s.hash = hex.EncodeToString(h[:])
	return s, nil
}

// scriptSum is the hex SHA-256 of the script rel in root, or "" when it is
// absent or is not an executable regular file. It returns a plain error when
// the root refuses the path, such as for a symlink out of it, and a
// *scriptError when an executable script cannot be read.
func scriptSum(root *os.Root, rel string) (string, error) {
	st, err := root.Stat(rel)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", nil
	case err != nil:
		return "", fmt.Errorf("read %s: %w", rel, err)
	case !st.Mode().IsRegular() || st.Mode()&0o111 == 0:
		return "", nil
	}
	unreadable := func(err error) (string, error) { return "", &scriptError{script: rel, err: err} }
	// Without blocking: a script swapped for a pipe must not hold the start.
	f, err := root.OpenFile(rel, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return unreadable(err)
	}
	defer f.Close()
	st, err = f.Stat()
	switch {
	case err != nil:
		return unreadable(err)
	case !st.Mode().IsRegular() || st.Mode()&0o111 == 0:
		return "", nil // it changed since the first look, and cannot run
	}
	sum := sha256.New()
	if _, err := io.Copy(sum, f); err != nil {
		return unreadable(err)
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
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

// hostHook is the host's own executable hook of this kind, or "".
func hostHook(kind string) string { return executable(hostHookPath(kind)) }

// hostHookPath is where the host's own hook of this kind is.
func hostHookPath(kind string) string { return filepath.Join(paths.ConfigDir(), "hooks", kind) }

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
// resuming is true when the agent starts again, after a restore or a resume.
func hookVars(m *Manifest, resuming bool) []string {
	kind, fresh := "setup", "0"
	if resuming {
		kind = "resume"
	}
	if m.Git != nil && m.Git.CreatedBy && !resuming {
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
