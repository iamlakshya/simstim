package host

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/proto"
)

func saveWalledTestManifest(t *testing.T, id, guestCwd, hostDir string) *Manifest {
	t.Helper()
	alertHome(t)
	fakeProc(t, map[string]string{"self/cgroup": outsideCgroup("")})
	userManager(t, "cgroup:[1]", "cgroup:[1]", nil)
	m := &Manifest{
		Schema:    1,
		ID:        id,
		Name:      "walled-sess",
		CreatedAt: time.Now().UTC(),
		Cwd:       guestCwd,
		Agent:     AgentInfo{Kind: "shell", Argv: []string{"/bin/sh"}},
		Wall: &WallRecord{
			Level:          "process",
			Driver:         "process",
			WorkspaceHost:  hostDir,
			WorkspaceGuest: guestCwd,
		},
	}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestManifestHostDir(t *testing.T) {
	unwalled := &Manifest{Cwd: "/home/user/proj"}
	if got := unwalled.HostDir(); got != "/home/user/proj" {
		t.Errorf("unwalled.HostDir() = %q, want /home/user/proj", got)
	}
	walled := &Manifest{
		Cwd:  "/work/proj",
		Wall: &WallRecord{Level: "process", Driver: "process", WorkspaceHost: "/home/user/proj", WorkspaceGuest: "/work/proj"},
	}
	if got := walled.HostDir(); got != "/home/user/proj" {
		t.Errorf("walled.HostDir() = %q, want /home/user/proj", got)
	}
	plain := &Manifest{
		Cwd:  "/home/user/proj",
		Wall: &WallRecord{Level: "none", Driver: "plain"},
	}
	if got := plain.HostDir(); got != "/home/user/proj" {
		t.Errorf("plain.HostDir() = %q, want /home/user/proj", got)
	}
}

func TestHostSFTPExecAndDialPortFailClosedOnWalledSessions(t *testing.T) {
	// Even when m.Cwd happens to exist on the host filesystem, host-side sftp,
	// ssh-exec and dial-port must refuse walled sessions with forbidden + Hint
	// and never execute anything on the host.
	existingDir := t.TempDir()
	marker := filepath.Join(existingDir, "should-not-exist")
	const id = "wall2345"
	saveWalledTestManifest(t, id, existingDir, existingDir)

	var out bytes.Buffer
	err := RunSFTP(id, strings.NewReader(""), &out)
	var pe *proto.Error
	if !errors.As(err, &pe) || pe.Code != "forbidden" || pe.Hint == "" {
		t.Fatalf("RunSFTP on walled session = %#v, want proto.Error{Code:forbidden, Hint:...}", err)
	}

	// Send a valid exec request trying to touch marker on the host; it must
	// be refused before anything runs.
	var execIn bytes.Buffer
	w := proto.NewWriter(&execIn)
	rawArgs, _ := json.Marshal(api.SSHExecArgs{Command: "touch '" + marker + "'"})
	_ = w.WriteJSON(proto.TRequest, proto.Request{ID: 1, Op: "exec", Args: rawArgs})
	out.Reset()
	err = RunSSHExec(id, &execIn, &out)
	pe = nil
	if !errors.As(err, &pe) || pe.Code != "forbidden" || pe.Hint == "" {
		t.Fatalf("RunSSHExec on walled session = %#v, want proto.Error{Code:forbidden, Hint:...}", err)
	}
	if _, serr := os.Stat(marker); !errors.Is(serr, os.ErrNotExist) {
		t.Fatalf("RunSSHExec ran a host command for a walled session (marker stat err=%v)", serr)
	}

	out.Reset()
	err = RunDialPort([]string{id, "127.0.0.1", "8080"}, strings.NewReader(""), &out)
	pe = nil
	if !errors.As(err, &pe) || pe.Code != "forbidden" || pe.Hint == "" {
		t.Fatalf("RunDialPort on walled session = %#v, want proto.Error{Code:forbidden, Hint:...}", err)
	}
	if !strings.Contains(out.String(), "wall") {
		t.Errorf("RunDialPort JSON reply = %q, want wall refusal", out.String())
	}
}

func TestPullBundleAndFoldersUseHostDirForWalledSession(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	// git names the repository by its real path, and on macOS the
	// temporary folder is under /var, a link to /private/var.
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	repo := filepath.Join(home, "myrepo")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	gitRun := func(args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(context.Background(), "git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	gitRun("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun("add", "file.txt")
	gitRun("commit", "-q", "-m", "initial")
	head := gitRun("rev-parse", "HEAD")

	// Build a temporary helper so gitOp can execute git-op in a subprocess.
	binDir := t.TempDir()
	helper := filepath.Join(binDir, brand.HostBinary)
	build := exec.CommandContext(context.Background(), "go", "build", "-buildvcs=false", "-o", helper, "../../cmd/host")
	if bOut, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build helper: %v\n%s", err, bOut)
	}
	defer SetExecutable(helper)()

	const id = "wgit2345"
	m := saveWalledTestManifest(t, id, "/work/myrepo", repo)

	// 1. RunPullBundle uses m.HostDir() (repo), NOT m.Cwd (/work/myrepo).
	var out bytes.Buffer
	if err := RunPullBundle(id, strings.NewReader(`{"full":true}`), &out); err != nil {
		t.Fatalf("RunPullBundle: %v", err)
	}
	br := bufio.NewReader(&out)
	line, err := br.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read header: %v", err)
	}
	var hdr api.PullBundleHeader
	if err := json.Unmarshal(line, &hdr); err != nil {
		t.Fatalf("decode header: %v (%q)", err, line)
	}
	if hdr.Error != nil {
		t.Fatalf("pull-bundle returned error: %+v", hdr.Error)
	}
	if hdr.Head != head || hdr.Size == 0 || hdr.Repo == nil || hdr.Repo.Branch != "main" {
		t.Fatalf("unexpected pull header: %+v (want head %s)", hdr, head)
	}

	// 2. Folders() recent list returns host path ("~/myrepo"), NOT "/work/myrepo".
	fr := Folders("", 12)
	if slices.Contains(fr.Recent, "/work/myrepo") {
		t.Fatalf("Folders().Recent leaked guest path /work/myrepo: %v", fr.Recent)
	}
	if !slices.Contains(fr.Recent, "~/myrepo") {
		t.Fatalf("Folders().Recent = %v, want ~/myrepo", fr.Recent)
	}

	// 3. stopHook and resumeHook verify repository scripts against m.HostDir().
	hookDir := filepath.Join(repo, "."+brand.Name)
	if err := os.MkdirAll(hookDir, 0o700); err != nil {
		t.Fatal(err)
	}
	stopScript := filepath.Join(hookDir, "stop")
	resumeScript := filepath.Join(hookDir, "resume")
	if err := os.WriteFile(stopScript, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resumeScript, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	sc, err := readScripts(repo)
	if err != nil {
		t.Fatal(err)
	}
	m.StopHook = stopScript
	m.ResumeHook = resumeScript
	m.SetupHash = sc.hash
	if got := stopHook(m); got != stopScript {
		t.Errorf("stopHook(walled) = %q, want %q", got, stopScript)
	}
	if got := resumeHook(m); got != resumeScript {
		t.Errorf("resumeHook(walled) = %q, want %q", got, resumeScript)
	}

	// 4. List() keeps Cwd as the guest path "/work/myrepo".
	list, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Cwd != "/work/myrepo" {
		t.Errorf("List() = %+v, want Cwd=/work/myrepo", list)
	}
}
