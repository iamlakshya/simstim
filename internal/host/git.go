package host

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/iamlakshya/simstim/internal/api"
)

// gitHookEvents are the hook events of githooks(5), as of git 2.54.0, and
// unchanged up to 2.56.1 (https://git-scm.com/docs/githooks).
var gitHookEvents = []string{
	"applypatch-msg", "pre-applypatch", "post-applypatch", "pre-commit", "pre-merge-commit",
	"prepare-commit-msg", "commit-msg", "post-commit", "pre-rebase", "post-checkout", "post-merge",
	"pre-push", "pre-receive", "update", "proc-receive", "post-receive", "post-update",
	"reference-transaction", "push-to-checkout", "pre-auto-gc", "post-rewrite", "sendemail-validate",
	"fsmonitor-watchman", "p4-changelist", "p4-prepare-changelist", "p4-post-changelist",
	"p4-pre-submit", "post-index-change",
}

// gitSafe starts every git command of the helper (spec 19.3). It turns off
// what a repository's config, which a session can write, makes git run:
// its fsmonitor, the hook scripts of its hooks folder, and from git 2.55
// every hook of each event, including the hooks that the config defines
// (`hook.<event>.enabled`, git-hook(1)). As a second guard, git() also turns
// off each hook that the config names. Git 2.54 has no switch for an event,
// and a hook that a session adds between that read and the command would
// run, so gitVersionGate refuses it.
var gitSafe = func() []string {
	args := make([]string, 0, 4+2*len(gitHookEvents))
	args = append(args, "-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null")
	for _, e := range gitHookEvents {
		args = append(args, "-c", "hook."+e+".enabled=false")
	}
	return args
}()

var gitVersionRE = regexp.MustCompile(`^git version (\d+)\.(\d+)(\.\d+)?`)

// gitVersionGate refuses git 2.54.x for an operation that can fire hooks.
// Git before 2.54 has no hooks in a repository's config, and from 2.55 the
// switch of each event turns them off. Git 2.54 runs them, and has no such
// switch: only the per-name read guards it, and a session can add a hook
// after that read. Without git, it says nothing, and the operation says
// no_git. A version that it cannot read is refused too.
func gitVersionGate() error {
	if !haveGit() {
		return nil
	}
	out, err := gitCommand(append(slices.Clone(gitSafe), "version")...).Output()
	m := gitVersionRE.FindStringSubmatch(strings.TrimSpace(string(out)))
	if err != nil || m == nil {
		return UserError(api.CodeGitVersion, fmt.Sprintf("could not read the version of git on this host: %q", firstLine(string(out))),
			"Check git on the host with `git version`, and update it to 2.55 or later.")
	}
	if m[1] == "2" && m[2] == "54" {
		return UserError(api.CodeGitVersion, "git "+strings.TrimPrefix(m[0], "git version ")+" on this host runs the hooks that a repository's config defines, and has no switch that turns them all off, so the helper does not run it in a repository",
			"Update git on the host to 2.55 or later.")
	}
	return nil
}

// haveGit reports whether git is on PATH.
func haveGit() bool {
	_, err := exec.LookPath("git")
	return err == nil
}

// gitArgs is the command line of git with args, in the folder dir, with the
// hooks that the config names, hooks, turned off one by one.
func gitArgs(dir string, hooks []string, args ...string) []string {
	off := make([]string, 0, 2*len(hooks))
	for _, h := range hooks {
		off = append(off, "-c", "hook."+h+".enabled=false")
	}
	return slices.Concat(gitSafe, off, []string{"-C", dir}, args)
}

// git runs git with args in the folder dir, and returns what it printed.
// Only the commands of a git operation call it, in its child (gitop.go).
// Each command first reads the hooks that git's config defines, which runs
// nothing, so that it can turn each one off.
func git(dir string, args ...string) (string, error) {
	hooks, err := configHooks(dir)
	if err != nil {
		return "", err
	}
	out, _, err := runGit(dir, hooks, args...)
	return out, err
}

// gitCtx bounds every git command. A git operation's child sets it to the
// operation's deadline (runGitOpChild), so that the deadline holds in the
// child itself, also when the bridge that started it has gone. Elsewhere it
// never ends.
var gitCtx atomic.Pointer[context.Context]

// gitContext is the context that bounds git commands now.
func gitContext() context.Context {
	if p := gitCtx.Load(); p != nil {
		return *p
	}
	return context.Background()
}

// gitCommand is a git command line under gitContext, whose end kills git.
// git, and whatever it starts, such as a filter, stay in the process group
// of the git operation's child, which stops all of them at its deadline
// (stopOwnProcesses).
func gitCommand(args ...string) *exec.Cmd {
	cmd := exec.CommandContext(gitContext(), "git", args...)
	cmd.WaitDelay = 2 * time.Second
	return cmd
}

// runGit runs git, and returns its output, its exit code and an error that
// says what git said.
func runGit(dir string, hooks []string, args ...string) (string, int, error) {
	cmd := gitCommand(gitArgs(dir, hooks, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	s := strings.TrimSpace(string(out))
	if err != nil {
		code := -1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return s, code, fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return s, 0, nil
}

// configHooks lists the names of the hooks that git's config defines for
// dir, as `hook.<name>.command` (git 2.54 and later). An older git has none
// to run. A name that a `-c` option cannot carry, or a config that git
// cannot read, is an error: git then runs nothing in dir.
func configHooks(dir string) ([]string, error) {
	out, code, err := runGit(dir, nil, "config", "-z", "--name-only", "--get-regexp", `^hook\..*\.command$`)
	if code == 1 && out == "" {
		return nil, nil // no such key
	}
	if err != nil {
		return nil, UserError(api.CodeGitHooks, "could not read the hooks that git's config defines in "+dir+" on this host: "+err.Error(),
			"Check the repository's config on the host with `git config --show-origin --list`.")
	}
	var names []string
	for _, key := range strings.Split(strings.Trim(out, "\x00"), "\x00") {
		name := strings.TrimSuffix(strings.TrimPrefix(key, "hook."), ".command")
		if name == "" || strings.ContainsRune(name, '=') || strings.ContainsFunc(name, unicode.IsControl) {
			return nil, UserError(api.CodeGitHooks, fmt.Sprintf("git's config in %s on this host defines a hook named %q, which the helper cannot turn off", dir, name),
				"Remove the hook on the host with `git config --remove-section 'hook.<name>'`, then try again.")
		}
		if !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	return names, nil
}
