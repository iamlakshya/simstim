package client

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/proto"
)

// The answer of the helper's repo.info becomes what pull needs, and each
// error becomes one with a hint that names the host.
func TestParseRepoInfo(t *testing.T) {
	h := Host{Name: "box"}
	r, err := parseRepoInfo(h, "~/src/app", []byte(`{"repo":{"root":"/home/dev/src/app","main":"/home/dev/src/app","branch":"auth-fix","dirty":3}}`+"\n"))
	if err != nil || r != (RepoInfo{Root: "/home/dev/src/app", Main: "/home/dev/src/app", Branch: "auth-fix", Dirty: 3}) {
		t.Fatalf("%+v %v", r, err)
	}
	for _, c := range []struct{ out, msg, hint string }{
		{`{"error":{"code":"not_a_repo","message":"~ is not inside a git repository"}}`, "~/src/app on box is not in a git repository", "Start the session in a repository"},
		{`{"error":{"code":"no_git","message":"git is not installed on this host"}}`, "git is not installed on box", "Install git on box"},
		{`{"error":{"code":"git_scope","message":"could not start git in a scope on this host: Failed to connect to bus"}}`, "on box: Failed to connect to bus", brand.Name + " host doctor box"},
		{`{"error":{"code":"git","message":"git status: fatal: bad index"}}`, "bad index", "git status on box"},
		{"fatal: not a git repository\n", "could not read what box said", "host add box"},
		{`{}`, "could not read what box said", "host add box"},
	} {
		_, err := parseRepoInfo(h, "~/src/app", []byte(c.out))
		var ue *UserError
		if !errors.As(err, &ue) || !strings.Contains(ue.Msg, c.msg) || !strings.Contains(ue.Hint, c.hint) {
			t.Errorf("%s: %v, want %q with a hint %q", c.out, err, c.msg, c.hint)
		}
	}
}

// The helper's git errors say "on this host". The client names the host,
// and a git_scope error's hint runs the doctor there. Other errors pass.
func TestHostError(t *testing.T) {
	for _, c := range []struct {
		in        proto.Error
		msg, hint string
	}{
		{proto.Error{Code: api.CodeGitScope, Message: "could not start git in a scope on this host: Failed to connect to bus", Hint: "Run the doctor."},
			"could not start git in a scope on box: Failed to connect to bus", brand.Name + " host doctor box"},
		{proto.Error{Code: api.CodeGitTimeout, Message: "git ran longer than 1m0s in repo.info on this host, so the helper stopped it", Hint: "A filter may hang."},
			"git ran longer than 1m0s in repo.info on box, so the helper stopped it", "A filter may hang."},
		{proto.Error{Code: api.CodeGitHooks, Message: "git's config in /r on this host defines a hook named \"a=b\", which the helper cannot turn off", Hint: "Remove it."},
			"git's config in /r on box defines a hook named \"a=b\", which the helper cannot turn off", "Remove it."},
		{proto.Error{Code: "not_found", Message: "no session on this host", Hint: "List them."}, "no session on this host", "List them."},
	} {
		ue := hostErrorOn("box", &c.in)
		if ue.Msg != c.msg || ue.Hint != c.hint || ue.Code != c.in.Code {
			t.Errorf("hostError(%+v) = %+v, want %q, %q", c.in, ue, c.msg, c.hint)
		}
	}
}

func TestFetchArgs(t *testing.T) {
	h := Host{Name: "box", SSH: "dev@box"}
	got := FetchArgs(h, "/home/dev/src/app", "auth-fix", "review/auth-fix", false)
	want := []string{"fetch", "--no-tags", "--no-recurse-submodules", "--upload-pack=git-upload-pack", "--", "dev@box:/home/dev/src/app", "refs/heads/auth-fix:refs/heads/review/auth-fix"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := FetchArgs(h, "/r", "b", "b", true); got[len(got)-1] != "+refs/heads/b:refs/heads/b" {
		t.Errorf("force: %q", got[3])
	}
}

func TestShellWord(t *testing.T) {
	for in, want := range map[string]string{"plain": "plain", "a b": "'a b'", "it's": `'it'\''s'`, "ControlPath=/x/%C": "'ControlPath=/x/%C'"} {
		if got := shellWord(in); got != want {
			t.Errorf("shellWord(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSessionRepoIgnoresHostGitConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake ssh script")
	}
	root := t.TempDir()
	hostHome := filepath.Join(root, "host-home")
	clientHome := filepath.Join(root, "client-home")
	binDir := filepath.Join(root, "bin")
	for _, d := range []string{hostHome, clientHome, binDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	marker := filepath.Join(root, "git-config-ran")
	probe := filepath.Join(binDir, "git-probe")
	if err := os.WriteFile(probe, []byte("#!/bin/sh\necho ran > '"+marker+"'\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	hostGitConfig := "[core]\n\tfsmonitor = " + probe + "\n\thooksPath = " + hostHome + "/hooks\n[uploadpack]\n\tpackObjectsHook = " + probe + "\n"
	if err := os.WriteFile(filepath.Join(hostHome, ".gitconfig"), []byte(hostGitConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	sshBin := filepath.Join(binDir, "ssh")
	sshScript := "#!/bin/sh\nfor a; do last=$a; done\nunset GIT_CONFIG_NOSYSTEM GIT_CONFIG_GLOBAL\nHOME='" + hostHome + "' XDG_CONFIG_HOME='" + hostHome + "/.config' exec sh -c \"$last\"\n"
	if err := os.WriteFile(sshBin, []byte(sshScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", clientHome)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(clientHome, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(clientHome, ".cache"))
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	remote := filepath.Join(hostHome, "repo")
	gitClean := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	gitClean(remote, "init", "-q", "-b", "topic")
	gitClean(remote, "commit", "-q", "--allow-empty", "-m", "one")
	if err := os.WriteFile(filepath.Join(remote, "dirty.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Repository-level fsmonitor must also be neutralized.
	gitClean(remote, "config", "core.fsmonitor", probe)

	h := Host{Name: "box", SSH: "dev@box"}
	if err := SaveHosts(context.Background(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	pinFor(t, h.Name)
	info, err := SessionRepo(context.Background(), h, remote)
	if err != nil {
		t.Fatalf("SessionRepo: %v", err)
	}
	realRemote, _ := filepath.EvalSymlinks(remote)
	realRoot, _ := filepath.EvalSymlinks(info.Root)
	if realRoot != realRemote || info.Branch != "topic" || info.Dirty != 1 {
		t.Fatalf("SessionRepo = %+v, want root %q branch topic dirty 1", info, realRemote)
	}

	local := filepath.Join(root, "local")
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatal(err)
	}
	gitClean(local, "init", "-q", "-b", "main")
	sshCmd, err := GitSSHCommand(t.Context(), h)
	if err != nil {
		t.Fatal(err)
	}
	fetch := exec.Command("git", append([]string{"-C", local}, FetchArgs(h, remote, "topic", "topic", false)...)...)
	fetch.Env = append(os.Environ(), "GIT_SSH_COMMAND="+sshCmd, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	if out, err := fetch.CombinedOutput(); err != nil {
		t.Fatalf("git fetch: %v\n%s", err, strings.TrimSpace(string(out)))
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("host git config probe ran (%v)", err)
	}
}
