package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
)

func TestForward(t *testing.T) {
	th := newTestHost(t)
	newSession(t, "web", "sleep", "60")
	fails(t, "listens on no TCP ports yet", "forward", "box/web")
	fails(t, `"x" is not a port`, "forward", "box/web", "x")
	out := ok(t, "forward", "box/web", "45678:3000", "3001")
	if !strings.Contains(out, "Forwarding http://localhost:45678 → box:3000\n") || !strings.Contains(out, "Press Ctrl+C to stop.") {
		t.Errorf("forward = %q", out)
	}
	log, _ := os.ReadFile(filepath.Join(th.h.Home, "ssh.log"))
	if !strings.Contains(string(log), "-N -o ExitOnForwardFailure=yes -L 127.0.0.1:45678:127.0.0.1:3000 -L 127.0.0.1:") {
		t.Errorf("ssh was run with %q", log)
	}
	// forward turns off every other forwarding, and keeps its own ports: it
	// does not clear the forwards, which would clear its -L options too. It
	// has a connection of its own, and reads ssh -G first.
	lines := sshLines(t, th)
	if len(lines) != 2 || !strings.Contains(lines[0], " -G -- box") {
		t.Errorf("forward ran ssh %d times, want ssh -G, then the forward: %q", len(lines), lines)
	}
	for _, line := range lines {
		if !strings.Contains(line, noForwarding+" ") || strings.Contains(line, noPorts) ||
			!strings.Contains(line, " -o ControlMaster=no -o ControlPath=none ") || !strings.Contains(line, " -o ClearAllForwardings=no ") {
			t.Errorf("forward ran ssh with %q, want %q, a connection of its own, and ClearAllForwardings=no", line, noForwarding)
		}
	}
	// A host reached without SSH cannot forward.
	th.ch.SSH = ""
	if err := saveHost(th.ch); err != nil {
		t.Fatal(err)
	}
	fails(t, "forward needs SSH", "forward", "box/web", "3000")
	for addr, want := range map[string]string{"": "127.0.0.1", "0.0.0.0": "127.0.0.1", "::": "[::1]", "::1": "[::1]", "fe80::1": "[fe80::1]", "192.0.2.7": "192.0.2.7"} {
		if got := remoteHost(addr); got != want {
			t.Errorf("remoteHost(%q) = %q, want %q", addr, got, want)
		}
	}
	if p := freePort(context.Background()); p == 0 || !free(context.Background(), p) {
		t.Errorf("freePort = %d", p)
	}
}

func TestPull(t *testing.T) {
	hosttest.SkipOnGit254(t)
	th := newTestHost(t)
	// pull asks the helper about the repository: git-op repo.info.
	th.h.Install(client.HelperVersion(), "")
	gitc := func(dir string, args ...string) string {
		t.Helper()
		out, err := runGit(dir, args...)
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return out
	}
	// The session works in a repository on the host, on its own branch.
	remote := filepath.Join(th.h.Home, "src", "app")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	gitc(remote, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(remote, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitc(remote, "add", "a.txt")
	gitc(remote, "commit", "-q", "-m", "first")
	local := filepath.Join(th.h.Root, "clone")
	gitc(th.h.Root, "clone", "-q", remote, local)
	gitc(remote, "checkout", "-q", "-b", "feature")
	if err := os.WriteFile(filepath.Join(remote, "b.txt"), []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitc(remote, "add", "b.txt")
	gitc(remote, "commit", "-q", "-m", "work")
	if err := os.WriteFile(filepath.Join(remote, "c.txt"), []byte("c\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	newSession(t, "feat", "sleep", "60") // in the home folder, not a repository
	out := ok(t, "new", "box", "--detach", "--name", "repo", "--dir", remote, "--", "sleep", "60")
	if !strings.Contains(out, "(repo, shell)") {
		t.Fatalf("new = %q", out)
	}

	t.Chdir(th.h.Root)
	fails(t, "not in a git repository", "pull", "box/repo")
	t.Chdir(local)
	out = ok(t, "pull", "box/repo")
	if !strings.HasPrefix(out, "Fetched feature from repo on box into feature: 1 commits that your current branch does not have.") ||
		!strings.Contains(out, "The session has 1 files with uncommitted changes") {
		t.Errorf("pull = %q", out)
	}
	if got := gitc(local, "log", "--format=%s", "-1", "feature"); got != "work" {
		t.Errorf("the local feature branch is at %q", got)
	}
	gitc(local, "checkout", "-q", "feature")
	fails(t, "is the branch you have checked out here", "pull", "box/repo")
	fails(t, "is not in a git repository", "pull", "box/feat")
	// Each ssh that pull runs, its own and the fetch's, forwards nothing.
	lines := sshLines(t, th)
	if !slices.ContainsFunc(lines, func(l string) bool { return strings.Contains(l, "upload-pack") }) {
		t.Errorf("the fetch did not use the client's ssh: %q", lines)
	}
	for _, line := range lines {
		if !strings.Contains(line, noForwarding+" ") || !strings.Contains(line, noPorts+" ") {
			t.Errorf("pull ran ssh with %q, want %q and %q", line, noForwarding, noPorts)
		}
	}
	// The host's git ran in the helper's git-op, over the client's SSH.
	log, _ := os.ReadFile(filepath.Join(th.h.Home, "ssh.log"))
	if !strings.Contains(string(log), "git-op repo.info") {
		t.Errorf("pull did not run git-op repo.info on the host: %s", log)
	}
}

// A host whose systemd cannot make git's scope refuses a new worktree. The
// client names the host in the error, and its hint runs the doctor there.
func TestNewNamesTheHostOfAGitScopeError(t *testing.T) {
	th := newTestHost(t)
	repo := filepath.Join(th.h.Home, "src", "app")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"commit", "-q", "--allow-empty", "-m", "first"}} {
		if out, err := runGit(repo, args...); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	socket := filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "systemd", "private")
	if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(socket, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	th.h.Script("systemctl", "echo running\n")
	th.h.Script("systemd-run", "echo 'Failed to connect to bus: No such file or directory' >&2\nexit 1\n")
	failsWithHint(t, "could not start git in a scope on box: Failed to connect to bus", brand.Name+" host doctor box",
		"new", "box", "--detach", "--dir", repo, "--worktree", "--", "sleep", "60")
}

// kill of a session whose worktree a scope failure kept names the host in
// its note, and gives the doctor's hint for that host.
func TestKillNamesTheHostOfAKeptWorktree(t *testing.T) {
	hosttest.SkipOnGit254(t)
	th := newTestHost(t)
	repo := filepath.Join(th.h.Home, "src", "app")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"commit", "-q", "--allow-empty", "-m", "first"}} {
		if out, err := runGit(repo, args...); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	m := startedRE.FindStringSubmatch(ok(t, "new", "box", "--detach", "--name", "kept", "--dir", repo, "--worktree", "--", "sleep", "60"))
	if m == nil {
		t.Fatal("new did not start the session")
	}
	socket := filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "systemd", "private")
	if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(socket, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	th.h.Script("systemctl", "echo running\n")
	th.h.Script("systemd-run", "echo 'Failed to connect to bus: No such file or directory' >&2\nexit 1\n")
	out := ok(t, "kill", "box/"+m[1])
	for _, want := range []string{"Kept the worktree ", "could not start git in a scope on box: Failed to connect to bus", "\nTry: " + brand.Name + " host doctor box\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("kill printed %q, without %q", out, want)
		}
	}
}

// The options of decision 0034, as the fake ssh logs them.
const (
	noForwarding = "-o ForwardAgent=no -o ForwardX11=no -o PermitLocalCommand=no -o GSSAPIDelegateCredentials=no"
	noPorts      = "-o ClearAllForwardings=yes"
)

// sshLines returns each command line that the fake ssh ran. It fails the
// test when ssh did not run.
func sshLines(t *testing.T, th *testHost) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(th.h.Home, "ssh.log"))
	if err != nil || len(b) == 0 {
		t.Fatalf("ssh did not run: %v", err)
	}
	// The client asks ssh once which options it knows, with no host and
	// no connection. That run may land in any test's log.
	return slices.DeleteFunc(strings.Split(strings.TrimSpace(string(b)), "\n"),
		func(l string) bool { return strings.HasSuffix(l, " -- probe.invalid") })
}

// A failure inside worktree.remove, here a hook in the config that the
// helper cannot turn off, keeps its code and hint: kill names the host, and
// says what to try.
func TestKillShowsWhyAWorktreeStays(t *testing.T) {
	hosttest.SkipOnGit254(t)
	newTestHost(t)
	repo := filepath.Join(os.Getenv("HOME"), "src", "app")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"commit", "-q", "--allow-empty", "-m", "first"}} {
		if out, err := runGit(repo, args...); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	m := startedRE.FindStringSubmatch(ok(t, "new", "box", "--detach", "--name", "bad", "--dir", repo, "--worktree", "--", "sleep", "60"))
	if m == nil {
		t.Fatal("new did not start the session")
	}
	f, err := os.OpenFile(filepath.Join(repo, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("[hook \"a=b\"]\n\tcommand = true\n")
	f.Close()
	out := ok(t, "kill", "box/"+m[1])
	for _, want := range []string{"Kept the worktree ", " on box defines a hook named \"a=b\"", "\nTry: Remove the hook on the host"} {
		if !strings.Contains(out, want) {
			t.Errorf("kill printed %q, without %q", out, want)
		}
	}
	// The repository is the test's own: let the cleanup remove it.
	f, _ = os.OpenFile(filepath.Join(repo, ".git", "config"), os.O_WRONLY|os.O_TRUNC, 0)
	_, _ = f.WriteString("[core]\n\trepositoryformatversion = 0\n")
	f.Close()
}

// runGit runs git in dir as a test user.
func runGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false"}, args...)...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}
