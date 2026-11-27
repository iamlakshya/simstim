package host

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
)

// gitScopeEnv is set in the environment of a git operation's child. It names
// the scope that holds the child, or gitNoScope. A git-op that finds it set
// runs the operation itself. Without it, as when the client runs git-op over
// SSH, git-op starts the child first.
var gitScopeEnv = brand.Env("GIT_SCOPE")

// gitNoScope is gitScopeEnv's value when the host has no systemd user
// manager, and so the child runs without a scope.
const gitNoScope = "none"

// gitScopeProbeTimeout bounds the question to the user manager. A project
// choice. Tests shorten it.
var gitScopeProbeTimeout = 5 * time.Second

// gitScopeHint is the next step after a git_scope error.
var gitScopeHint = "Run `" + brand.Name + " host doctor` to check the host's systemd user manager."

// userManagerSocket is the private socket of the user's systemd manager,
// which exists while the manager runs.
func userManagerSocket() string {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = fmt.Sprintf("/run/user/%d", os.Getuid())
	}
	return filepath.Join(dir, "systemd", "private")
}

// gitScopeWanted says whether git runs in a scope: yes with a systemd user
// manager, no without one. It fails closed. When the manager's socket
// exists, the helper believes only a whole answer: systemctl ended in time,
// its output closed, and it exited with code 0 and the state "running", or
// with code 1 and the state "degraded", "starting" or "offline". Anything
// else, such as a deadline, output that did not close, another exit code or
// another state, means that the helper cannot tell where git would run:
// git_scope.
func gitScopeWanted() (bool, error) {
	if _, err := os.Lstat(userManagerSocket()); errors.Is(err, fs.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, gitScopeError("could not look for its systemd user manager: " + err.Error())
	}
	out, code, err := probeSystemd()
	if err != nil {
		return false, gitScopeError("could not ask its systemd user manager whether it runs: " + err.Error())
	}
	// is-system-running exits with 1 for a state other than running, and
	// still prints the state.
	switch state := strings.TrimSpace(out); {
	case code == 0 && state == "running", code == 1 && (state == "degraded" || state == "starting"):
		return true, nil
	case code == 1 && state == "offline": // the manager does not run
		return false, nil
	case state == "":
		return false, gitScopeError(fmt.Sprintf("could not ask its systemd user manager whether it runs: it gave no state, and exit code %d", code))
	default:
		return false, gitScopeError(fmt.Sprintf("could not ask its systemd user manager whether it runs: it is %s, with exit code %d", state, code))
	}
}

// probeSystemd runs `systemctl --user is-system-running`, and returns what
// it printed and its exit code. It reads the output itself, so it knows
// that the output closed, whatever the exit code: a process that systemctl
// left behind with the output open makes the answer incomplete. That, the
// deadline, a signal and a systemctl that does not start are errors.
func probeSystemd() (string, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitScopeProbeTimeout)
	defer cancel()
	r, w, err := os.Pipe()
	if err != nil {
		return "", 0, fmt.Errorf("make a pipe: %w", err)
	}
	defer r.Close()
	cmd := exec.CommandContext(ctx, "systemctl", "--user", "is-system-running")
	cmd.Stdout = w
	cmd.WaitDelay = time.Second
	err = cmd.Start()
	w.Close()
	if err != nil {
		return "", 0, fmt.Errorf("start systemctl: %w", err)
	}
	type read struct {
		b   []byte
		err error
	}
	got := make(chan read, 1)
	go func() {
		b, err := io.ReadAll(r)
		got <- read{b, err}
	}()
	waitErr := cmd.Wait()
	var out read
	select {
	case out = <-got:
	case <-ctx.Done():
	case <-time.After(time.Second): // systemctl ended, and its output stays open
		return "", 0, errors.New("its output did not close after it ended")
	}
	switch {
	case ctx.Err() != nil:
		return "", 0, errors.New("it did not answer within " + gitScopeProbeTimeout.String())
	case out.err != nil:
		return "", 0, fmt.Errorf("read its answer: %w", out.err)
	}
	var ee *exec.ExitError
	switch {
	case waitErr == nil:
		return string(out.b), 0, nil
	case errors.As(waitErr, &ee) && ee.Exited():
		return string(out.b), ee.ExitCode(), nil
	}
	return "", 0, fmt.Errorf("systemctl: %w", waitErr) // a signal
}

// gitScopeError is a git_scope error that says why.
func gitScopeError(why string) error {
	return UserError(api.CodeGitScope, "could not start git in a scope on this host: "+why, gitScopeHint)
}

// gitOpArgv is the command line of a git operation's child, and the scope
// that holds it, or gitNoScope. With systemd, the child runs in a new scope
// under the sessions' slice.
func gitOpArgv(exe, op string, systemd bool) (scope string, argv []string) {
	argv = []string{exe, "git-op", op}
	if !systemd {
		return gitNoScope, argv
	}
	scope = newScopeName()
	return scope, append(scopeArgv(scope), argv...)
}

// newScopeName names a new scope for git: <name>-git-<8 hex>.scope.
func newScopeName() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return brand.Name + "-git-" + hex.EncodeToString(b[:]) + ".scope"
}

// scopeArgv is systemd-run's command line for a new scope, before the
// command that runs in it.
func scopeArgv(unit string) []string {
	return []string{"systemd-run", "--user", "--scope", "--quiet", "--collect", "--slice=" + brand.Slice, "--unit=" + unit}
}

// stopScope stops a scope, and so every process in it.
func stopScope(unit string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _ = systemctl(ctx, "stop", unit)
}

// gitScopeCheck is the host doctor's line about git's scope: whether
// systemd-run can make a scope under the sessions' slice, as each git
// operation does. Without a systemd user manager there is no line.
func gitScopeCheck() []Check {
	const name = "git scope"
	scoped, err := gitScopeWanted()
	if err != nil {
		return []Check{{Name: name, Detail: err.Error(), Fix: "systemctl --user status"}}
	}
	if !scoped {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	argv := append(scopeArgv(newScopeName()), "true")
	out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).CombinedOutput()
	if err != nil {
		why := firstLine(string(out))
		if why == "" {
			why = err.Error()
		}
		return []Check{{Name: name, Detail: "systemd-run cannot make a scope under " + brand.Slice + ", so no git step can run: " + why,
			Fix: "systemd-run --user --scope --slice=" + brand.Slice + " true"}}
	}
	return []Check{{Name: name, OK: true, Detail: "the helper's git runs in a scope under " + brand.Slice}}
}

// firstLine is the first line of s that is not empty, trimmed.
func firstLine(s string) string {
	for line := range strings.Lines(s) {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}
