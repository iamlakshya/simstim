//go:build unix

package cli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
)

// The session commands print a session's name, which the host keeps as it
// was given, as plain text: new --detach, ports, send, debug, rename, kill,
// resume, and attach to an agent that has ended.
func TestSessionCommandsCleanHostText(t *testing.T) {
	newTestHost(t)
	const name = "app\x1b]52;c;aGk=\x07\u202egnp"
	const shown = "app]52;c;aGk=gnp"
	check := func(what, out string) {
		t.Helper()
		noHostBits(t, what, out)
		if !strings.Contains(out, shown) {
			t.Errorf("%s does not show the name %q:\n%s", what, shown, out)
		}
	}
	out := okSaid(t, "new", "box", "--detach", "--wall", "none", "--downgrade", "--name", name, "--", "/bin/sh", "-c", "exec sleep 60")
	check("new --detach", out)
	id := startedRE.FindStringSubmatch(out)[1]
	check("ports", okSaid(t, "ports", "box/"+id))
	check("send", okSaid(t, "send", "box/"+id, "true", "--no-enter"))
	out = okSaid(t, "debug", "start", "box/"+id, "--for", "10m")
	check("debug start", out)
	if !strings.Contains(out, "debug stop box/"+id) {
		t.Errorf("debug start names the session by its name, which is not the one shown:\n%s", out)
	}
	check("debug stop", okSaid(t, "debug", "stop", "box/"+id))
	check("kill", okSaid(t, "kill", "box/"+id))

	id = newSession(t, name, "/bin/sh", "-c", "exec sleep 60")
	check("rename", okSaid(t, "rename", "box/"+id, "plain"))

	id = newSession(t, name, "/bin/sh", "-c", "sleep 1; exit 2")
	agentEnded(t, id)
	t.Setenv(noInputEnv, "")
	term, done := inTerminal(t, "attach", "box/"+id)
	term.WaitFor("Start shell again in ")
	term.Type("n\r")
	err := wait(t, done)
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Kind != client.KindCancelled {
		t.Fatalf("attach to an ended agent = %v", err)
	}
	check("attach to an ended agent", ue.Msg)
	check("resume", okSaid(t, "resume", "box/"+id, "--detach"))
}

// A clean filter in the session's repository writes the OSC 52 clipboard
// sequence and more to stderr, and fails. git's error, which pull prints,
// passes the cleaner: the text arrives, without its control bytes.
func TestPullCleansAFiltersError(t *testing.T) {
	hosttest.SkipOnGit254(t)
	th := newTestHost(t)
	th.h.Install(client.HelperVersion(), "")
	remote := filepath.Join(th.h.Home, "src", "app")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	git := func(dir string, args ...string) {
		t.Helper()
		if out, err := runGit(dir, args...); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	for name, body := range map[string]string{"README": "app\n", ".gitattributes": "README filter=evil\n"} {
		if err := os.WriteFile(filepath.Join(remote, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(remote, "init", "-q", "-b", "main")
	git(remote, "add", "README", ".gitattributes")
	git(remote, "commit", "-q", "-m", "first")
	said := filepath.Join(th.h.Root, "said")
	filter := filepath.Join(th.h.Root, "evil.sh")
	if err := os.WriteFile(said, []byte("fatal: "+hostile), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filter, []byte("#!/bin/sh\ncat "+said+" >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	git(remote, "config", "filter.evil.clean", filter)
	git(remote, "config", "filter.evil.required", "true")
	// A new time, so that git status reads the file through the filter.
	later := time.Now().Add(10 * time.Second)
	if err := os.Chtimes(filepath.Join(remote, "README"), later, later); err != nil {
		t.Fatal(err)
	}
	m := startedRE.FindStringSubmatch(okSaid(t, "new", "box", "--detach", "--wall", "none", "--downgrade", "--name", "evil", "--dir", remote, "--", "sleep", "60"))
	if m == nil {
		t.Fatal("new did not start the session")
	}
	local := t.TempDir()
	git(local, "init", "-q", "-b", "main")
	t.Chdir(local)
	_, err := run(t, "pull", "box/"+m[1])
	var ue *client.UserError
	if !errors.As(err, &ue) || !strings.Contains(ue.Msg, "fatal: ]52;c;aGk=Allow this?") || ue.Hint == "" {
		t.Fatalf("pull = %v, want git's error with the filter's text, cleaned", err)
	}
	noHostBits(t, "pull's error", ue.Msg+"\n"+ue.Hint)
}

// pull's two early refusals print the session's name and its branch, both
// the host's text, as plain text: a session with no branch, and a branch
// that is checked out here.
func TestPullRefusalsCleanHostText(t *testing.T) {
	// pull asks the helper about the repository (git-op repo.info), which
	// git 2.54 may not count the changes of.
	hosttest.SkipOnGit254(t)
	th := newTestHost(t)
	th.h.Install(client.HelperVersion(), "")
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.CommandContext(context.Background(), "git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	const branch = "feat\u202eppa\u200b"
	remote, local := t.TempDir(), t.TempDir()
	for _, dir := range []string{remote, local} {
		git(dir, "init", "-q", "-b", branch)
		git(dir, "commit", "-q", "--allow-empty", "-m", "one")
	}
	t.Chdir(local)
	out := okSaid(t, "new", "box", "--detach", "--wall", "none", "--downgrade", "--name", "app"+hostile, "--dir", remote, "--", "/bin/sh", "-c", "exec sleep 60")
	id := startedRE.FindStringSubmatch(out)[1]
	refusal := func(what, want string) {
		t.Helper()
		_, err := run(t, "pull", "box/"+id)
		var ue *client.UserError
		if !errors.As(err, &ue) || !strings.Contains(ue.Msg, want) {
			t.Fatalf("pull (%s) = %v, want %q", what, err, want)
		}
		noHostBits(t, what, ue.Msg)
	}
	refusal("a branch checked out here", "featppa is the branch you have checked out here")
	git(remote, "checkout", "-q", "--detach")
	refusal("a detached HEAD", "app]52;c;aGk=Allow this? has no branch checked out")
}
