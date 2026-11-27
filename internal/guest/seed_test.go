//go:build linux || darwin

package guest

import (
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

func fixtureGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("/usr/bin/git", host.SafeGitArgs(dir, args...)...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture Git: %v: %s", err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestGuestClonesHistoryAndCreatesItsOwnBranch(t *testing.T) {
	source := t.TempDir()
	fixtureGit(t, source, "init", "--template=", "--initial-branch=chosen")
	for _, revision := range []string{"one", "two"} {
		if err := os.WriteFile(filepath.Join(source, "tracked"), []byte(revision), 0600); err != nil {
			t.Fatal(err)
		}
		fixtureGit(t, source, "add", "tracked")
		fixtureGit(t, source, "commit", "-m", revision)
	}
	commit := fixtureGit(t, source, "rev-parse", "HEAD")
	bundle := filepath.Join(t.TempDir(), "seed.bundle")
	fixtureGit(t, source, "bundle", "create", bundle, "chosen")
	data, err := os.ReadFile(bundle)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for _, dir := range []string{"work", "tmp", "home/agent"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	left, right := net.Pipe()
	sender := link.New(left, link.Supervisor, link.Options{})
	receiver := link.New(right, link.Guest, link.Options{})
	defer sender.Close()
	defer receiver.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := sender.Open(ctx, link.KindBundle, map[string]string{"token": "fixture2"})
	if err != nil {
		t.Fatal(err)
	}
	incoming, err := receiver.Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	defer incoming.Close()
	transferred := make(chan error, 1)
	go func() {
		_, err := out.Write(append(data, []byte("INCLUDED=1")...))
		if err == nil {
			err = out.CloseWrite()
		}
		transferred <- err
	}()
	branch := brand.Name + "/fixture2/test"
	g := &Init{root: root, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	err = g.cloneSeed(proto.CtlSeedArgs{Token: "fixture2", Repo: "project", Branch: branch, SourceBranch: "chosen", Commit: commit, Size: int64(len(data)), Includes: []proto.SeedFile{{Path: ".env", Size: 10}}}, incoming)
	if err != nil {
		t.Fatal(err)
	}
	if err = <-transferred; err != nil {
		t.Fatal(err)
	}
	clone := filepath.Join(root, "work/project")
	if got := fixtureGit(t, clone, "symbolic-ref", "--short", "HEAD"); got != branch {
		t.Fatal(got)
	}
	if got := fixtureGit(t, clone, "log", "--format=%s"); got != "two\none" {
		t.Fatal(got)
	}
	included, err := os.ReadFile(filepath.Join(clone, ".env"))
	if err != nil || string(included) != "INCLUDED=1" {
		t.Fatalf("%q %v", included, err)
	}
	// A second start sees the preserved clone and asks for no seed stream.
	if result, err := g.seed(proto.CtlSeedArgs{Token: "fixture2", Repo: "project"}); err != nil || result.Needed {
		t.Fatalf("resume replaced workspace: %+v %v", result, err)
	}
}
