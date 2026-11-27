package pullexport

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/gitpolicy"
)

type nativeBoundarySource struct {
	seed, ref     string
	partial, full []byte
	captures      []CaptureArgs
	closed        bool
}

func (s *nativeBoundarySource) PullMax() int64      { return 1 << 20 }
func (s *nativeBoundarySource) SeedHeads() []string { return []string{s.seed} }
func (s *nativeBoundarySource) Capture(ctx context.Context, args CaptureArgs, out io.Writer) (CaptureResult, error) {
	s.captures = append(s.captures, args)
	data := s.partial
	if args.Full {
		data = s.full
	}
	_, err := out.Write(data)
	return CaptureResult{DefaultRef: s.ref, Full: args.Full}, err
}
func (*nativeBoundarySource) RecordPulled(context.Context, []string) error { return nil }
func (s *nativeBoundarySource) Close() error                               { s.closed = true; return nil }

func boundaryGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.CommandContext(t.Context(), "git", append(gitpolicy.Args(), append([]string{"-C", dir}, args...)...)...)
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
	data, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("native boundary fixture: %v %s", err, data)
	}
	return strings.TrimSuffix(string(data), "\n")
}

func TestNativeDivergentBoundaryRecapturesOnceWithoutPublishingForeignPrerequisite(t *testing.T) {
	repo := t.TempDir()
	boundaryGit(t, repo, "init", "--template=", "-b", "main")
	boundaryGit(t, repo, "commit", "--allow-empty", "-m", "ancestor")
	ancestor := boundaryGit(t, repo, "rev-parse", "HEAD")
	boundaryGit(t, repo, "commit", "--allow-empty", "-m", "seed")
	seed := boundaryGit(t, repo, "rev-parse", "HEAD")
	ref := "refs/heads/" + brand.Name + "/primary2/topic"
	boundaryGit(t, repo, "checkout", "-b", strings.TrimPrefix(ref, "refs/heads/"), ancestor)
	boundaryGit(t, repo, "commit", "--allow-empty", "-m", "divergent work")
	partialPath, fullPath := filepath.Join(t.TempDir(), "partial.bundle"), filepath.Join(t.TempDir(), "full.bundle")
	boundaryGit(t, repo, "bundle", "create", partialPath, ref, "^"+seed)
	boundaryGit(t, repo, "bundle", "create", fullPath, ref)
	partial, err := os.ReadFile(partialPath)
	if err != nil {
		t.Fatal(err)
	}
	full, err := os.ReadFile(fullPath)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(partialPath)
	if err != nil {
		t.Fatal(err)
	}
	header, err := gitpolicy.ReadBundleHeader(file, 1<<20)
	file.Close()
	if err != nil || len(header.Prerequisites) != 1 || header.Prerequisites[0] != ancestor || ancestor == seed {
		t.Fatal("native divergent boundary positive control did not arise", header, err)
	}
	source := &nativeBoundarySource{seed: seed, ref: ref, partial: partial, full: full}
	spool := t.TempDir()
	manager, err := New(t.Context(), Options{Directory: func(string) (string, error) { return spool, nil }, Open: func(context.Context, string, string) (Source, error) { return source, nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	descriptor, err := manager.Begin(t.Context(), "outer", "primary2", api.PullBeginArgs{ID: "fixture2", Token: strings.Repeat("a", 26)}, 1<<20)
	if err != nil || !descriptor.Full || len(source.captures) != 2 || source.captures[0].Full || !source.captures[1].Full || len(source.captures[1].Heads) != 1 {
		t.Fatal("foreign boundary did not use one same-owner full capture", descriptor, err, source.captures)
	}
	chunk, err := manager.Read(t.Context(), "outer", api.PullReadArgs{ID: descriptor.ID, Token: descriptor.Token})
	if err != nil || !chunk.EOF {
		t.Fatal(err)
	}
	verified := filepath.Join(t.TempDir(), "verified.bundle")
	if err = os.WriteFile(verified, chunk.Data, 0600); err != nil {
		t.Fatal(err)
	}
	local := t.TempDir()
	boundaryGit(t, local, "init", "--bare", "--template=")
	boundaryGit(t, local, "bundle", "verify", verified)
}
