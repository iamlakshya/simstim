package client

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/gitpolicy"
)

func pullFixtureGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("Git is needed for native bundle witnesses")
	}
	command := exec.CommandContext(t.Context(), git, append(gitpolicy.Args(), append([]string{"-C", dir}, args...)...)...)
	command.Env = append(gitFixtureMetadataEnvironment(t), "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture Git %s: %v: %s", args[0], err, output)
	}
	return gitLine(output)
}

func pullFixtureCommit(t *testing.T, dir, path, value string) {
	t.Helper()
	full := filepath.Join(dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
	pullFixtureGit(t, dir, "add", "--", path)
	pullFixtureGit(t, dir, "commit", "-m", "fixture change")
}

type nativePullFixture struct {
	source, local, base, tip, ref string
	partial, full                 []byte
}

func makeNativePull(t *testing.T, path, value string, foreign bool) nativePullFixture {
	t.Helper()
	source := t.TempDir()
	pullFixtureGit(t, source, "init", "--template=", "--initial-branch=main")
	pullFixtureCommit(t, source, "base.txt", "base")
	base := pullFixtureGit(t, source, "rev-parse", "HEAD")
	local := filepath.Join(t.TempDir(), "local")
	pullFixtureGit(t, t.TempDir(), "clone", "--template=", "--", source, local)
	ref := pullNamespace("primary2") + "topic"
	pullFixtureGit(t, source, "checkout", "-b", strings.TrimPrefix(ref, "refs/heads/"))
	pullFixtureCommit(t, source, path, value)
	tip := pullFixtureGit(t, source, "rev-parse", "HEAD")
	refs := []string{ref}
	if foreign {
		pullFixtureGit(t, source, "branch", "foreign")
		pullFixtureGit(t, source, "tag", "bait")
		refs = append(refs, "refs/heads/foreign", "refs/tags/bait")
	}
	makeBundle := func(name string, full bool) []byte {
		file := filepath.Join(t.TempDir(), name)
		args := append([]string{"bundle", "create", file}, refs...)
		if !full {
			args = append(args, "--not", base)
		}
		pullFixtureGit(t, source, args...)
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	return nativePullFixture{source: source, local: local, base: base, tip: tip, ref: ref, partial: makeBundle("partial.bundle", false), full: makeBundle("full.bundle", true)}
}

func nativePullPeer(f nativePullFixture) *pullPeerFixture {
	peer := &pullPeerFixture{supported: true, data: f.partial}
	peer.begin = func(_ context.Context, request api.PullBeginArgs, descriptor *api.PullExport) error {
		peer.data = f.partial
		if request.Full {
			peer.data = f.full
		}
		descriptor.Size = int64(len(peer.data))
		descriptor.SeedHeads = []string{f.base}
		descriptor.DefaultRef = f.ref
		return nil
	}
	return peer
}

func localRefs(t *testing.T, dir string) string {
	t.Helper()
	return pullFixtureGit(t, dir, "for-each-ref", "--format=%(refname) %(objectname)")
}
