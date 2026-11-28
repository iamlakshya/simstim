//go:build linux

package supervisor_test

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/supervisor"
	"github.com/iamlakshya/simstim/internal/version"
	"github.com/iamlakshya/simstim/internal/wall/process"
)

func TestProcessWallSFTPExecAndPullEndToEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	probe := process.Driver{}.Probe(ctx)
	if !probe.OK {
		t.Skipf("process wall unavailable: %s (%s)", probe.Reason, probe.Fix)
	}

	// Build cmd/host BEFORE hosttest.New(t) (which replaces PATH) and pin its
	// version to the test binary's version.Version so internal/link's version
	// check passes against our in-process supervisor.
	helperBin := filepath.Join(t.TempDir(), brand.HostBinary)
	ldflags := "-X github.com/iamlakshya/" + brand.Name + "/internal/version.Version=" + version.Version
	buildCmd := exec.CommandContext(ctx, "go", "build", "-buildvcs=false", "-ldflags", ldflags, "-o", helperBin, "../../cmd/host")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("build cmd/host: %v\n%s", err, out)
	}

	th := hosttest.New(t)
	h, tr := th.ClientHost("box")

	wsHost := filepath.Join(th.Home, "m1")
	if err := os.MkdirAll(wsHost, 0o700); err != nil {
		t.Fatal(err)
	}
	// Make wsHost a git repo so pull works too.
	if _, err := exec.LookPath("git"); err == nil {
		gitRun := func(dir string, args ...string) {
			t.Helper()
			cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
			cmd.Env = append(os.Environ(),
				"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
				"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
			)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
		}
		gitRun(wsHost, "init", "-q", "-b", "feature-m1")
		if err := os.WriteFile(filepath.Join(wsHost, "README.md"), []byte("v1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		gitRun(wsHost, "add", "README.md")
		gitRun(wsHost, "commit", "-q", "-m", "initial")
	}

	const id = "pwsf2345"
	now := time.Now().UTC()
	m := &host.Manifest{
		Schema:    1,
		ID:        id,
		Name:      "m1",
		CreatedAt: now,
		Agent:     host.AgentInfo{Kind: "shell", Argv: []string{"/bin/sh", "-c", "sleep 60"}},
		Cwd:       "/work/m1",
		Env: []string{
			"HOME=" + process.GuestHome,
			"USER=agent",
			"LOGNAME=agent",
			"LANG=C.UTF-8",
			"PATH=/usr/local/bin:/usr/bin:/bin",
			"TERM=xterm-256color",
			"HTTP_PROXY=" + process.GuestProxyURL,
			"HTTPS_PROXY=" + process.GuestProxyURL,
			"NO_PROXY=localhost,127.0.0.1,::1",
		},
		Term:   host.TermInfo{Term: "xterm-256color", Cols: 80, Rows: 24},
		Run:    host.Runtime{Socket: paths.Socket(id), BootID: host.BootID()},
		Status: host.Status{State: host.StateStarting, Since: now},
		Wall: &host.WallRecord{
			Level:          "process",
			Driver:         process.DriverName,
			Version:        probe.Version,
			Preset:         string(egress.PresetLocked),
			WorkspaceHost:  wsHost,
			WorkspaceGuest: "/work/m1",
			At:             now,
		},
	}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}

	sb := supervisor.NewProcessSandbox(id, helperBin, m)
	supDone := make(chan error, 1)
	go func() {
		supDone <- supervisor.RunWith(id, sb, supervisor.Options{
			HeartbeatTimeout: 10 * time.Second,
			TickInterval:     200 * time.Millisecond,
			StopGrace:        3 * time.Second,
			EgressExe:        helperBin,
		})
	}()
	t.Cleanup(func() {
		_, _ = host.HolderRequest(id, "kill", nil, 5*time.Second)
		select {
		case <-supDone:
		case <-time.After(10 * time.Second):
		}
		_ = os.RemoveAll(paths.SessionDir(id))
	})

	hosttest.WaitFor(t, 15*time.Second, "process-wall supervisor working", func() bool {
		lm, err := host.LoadManifest(id)
		return err == nil && lm.Status.State != host.StateStarting
	})

	// 1. Copy a file and a folder into /work/m1 and back out over SFTP inside bwrap.
	sess := &client.Session{ID: id, Name: "m1", Cwd: "/work/m1", Wall: &api.WallInfo{Level: "process"}}
	sc, closer, err := client.OpenSessionSFTP(ctx, h, sess)
	if err != nil {
		t.Fatalf("OpenSessionSFTP(process wall): %v", err)
	}
	defer closer.Close()

	wd, err := sc.Getwd()
	if err != nil || wd != "/work/m1" {
		t.Fatalf("process-wall sftp Getwd = %q (%v), want /work/m1", wd, err)
	}
	// Verify SFTP inside the wall cannot read host-only /etc/shadow.
	if _, err := sc.Open("/etc/shadow"); err == nil {
		t.Fatal("SFTP inside process wall opened /etc/shadow!")
	}

	laptopDir := t.TempDir()
	inFile := filepath.Join(laptopDir, "in.txt")
	if err := os.WriteFile(inFile, []byte("hello-process-wall\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CopyToSFTP(sc, inFile, "in.txt", client.CopyOptions{}); err != nil {
		t.Fatalf("CopyToSFTP in.txt: %v", err)
	}
	dirSrc := filepath.Join(laptopDir, "tree")
	if err := os.MkdirAll(filepath.Join(dirSrc, "d1"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirSrc, "d1", "leaf.txt"), []byte("leaf-data\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CopyToSFTP(sc, dirSrc, "tree", client.CopyOptions{}); err != nil {
		t.Fatalf("CopyToSFTP tree: %v", err)
	}
	outDir := filepath.Join(laptopDir, "out")
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CopyFromSFTP(sc, "in.txt", outDir, client.CopyOptions{}); err != nil {
		t.Fatalf("CopyFromSFTP in.txt: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(outDir, "in.txt")); err != nil || string(b) != "hello-process-wall\n" {
		t.Fatalf("out/in.txt = %q, %v", b, err)
	}

	// 2. Run exec inside the process wall and verify uid=1000, pwd=/work/m1, and copied files are visible.
	backend := &client.SessionSSHBackend{Host: h, SessionID: id, Transport: tr, Walled: true}
	es, err := backend.StartExec(ctx, api.SSHExecArgs{Command: `printf '%s|%s|%s|%s\n' "$(id -u)" "$(pwd)" "$(cat /work/m1/in.txt)" "$(cat /work/m1/tree/d1/leaf.txt)"`})
	if err != nil {
		t.Fatalf("StartExec inside process wall: %v", err)
	}
	_ = es.Stdin().Close()
	execOut, _ := io.ReadAll(es.Stdout())
	execCode, _ := es.Wait()
	_ = es.Close()
	if execCode != 0 || string(execOut) != "1000|/work/m1|hello-process-wall|leaf-data\n" {
		t.Fatalf("process-wall exec code=%d out=%q", execCode, execOut)
	}

	// 3. Pull the walled session's git branch into a local repo via PullBundleOn.
	if _, err := exec.LookPath("git"); err == nil {
		localRepo := t.TempDir()
		initLocal := exec.CommandContext(ctx, "git", "-C", localRepo, "init", "-q", "-b", "main")
		if out, err := initLocal.CombinedOutput(); err != nil {
			t.Fatalf("git init local: %v\n%s", err, out)
		}
		res, err := client.PullBundleOn(ctx, tr, h, sess, client.PullBundleOptions{
			LocalRepo: localRepo,
			Into:      "pulled-m1",
		})
		if err != nil {
			t.Fatalf("PullBundleOn(process wall): %v", err)
		}
		if res.Branch != "feature-m1" || res.Into != "pulled-m1" {
			t.Fatalf("PullBundleOn result = %+v", res)
		}
		show := exec.CommandContext(ctx, "git", "-C", localRepo, "show", "pulled-m1:README.md")
		if out, err := show.Output(); err != nil || strings.TrimSpace(string(out)) != "v1" {
			t.Fatalf("pulled README.md = %q (%v)", out, err)
		}
	}
}
