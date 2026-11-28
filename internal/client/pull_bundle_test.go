package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

type bundleProc struct {
	inR  *io.PipeReader
	inW  *io.PipeWriter
	outR *io.PipeReader
	outW *io.PipeWriter
	done chan error
}

func (p *bundleProc) Stdin() io.WriteCloser { return p.inW }
func (p *bundleProc) Stdout() io.Reader     { return p.outR }
func (p *bundleProc) Wait() error           { return <-p.done }
func (p *bundleProc) Kill()                 { _ = p.inW.Close(); _ = p.outW.Close() }
func (p *bundleProc) Stderr() string        { return "" }

type fakeBundleTransport struct {
	repoDir string
	branch  string
	calls   []api.PullBundleArgs
}

func (f *fakeBundleTransport) Run(context.Context, string, io.Reader) ([]byte, error) {
	return nil, errors.New("unused")
}
func (f *fakeBundleTransport) ReachHint() string { return "check host" }

func (f *fakeBundleTransport) Start(ctx context.Context, _ string) (Proc, error) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	p := &bundleProc{inR: inR, inW: inW, outR: outR, outW: outW, done: make(chan error, 1)}
	go func() {
		defer outW.Close()
		var req api.PullBundleArgs
		_ = json.NewDecoder(inR).Decode(&req)
		f.calls = append(f.calls, req)

		gitOut := func(args ...string) (string, error) {
			cmd := exec.CommandContext(ctx, "git", append([]string{"-C", f.repoDir}, args...)...)
			b, err := cmd.CombinedOutput()
			return strings.TrimSpace(string(b)), err
		}
		head, _ := gitOut("rev-parse", "refs/heads/"+f.branch)
		full := req.Full || req.Base == ""
		if !full && req.Base == head {
			hdr := api.PullBundleHeader{
				Repo:  &api.RepoInfo{Root: f.repoDir, Main: f.repoDir, Branch: f.branch},
				Head:  head,
				Empty: true,
			}
			b, _ := json.Marshal(hdr)
			_, _ = outW.Write(append(b, '\n'))
			p.done <- nil
			return
		}
		if !full {
			if _, err := gitOut("merge-base", "--is-ancestor", req.Base, "refs/heads/"+f.branch); err != nil {
				full = true
			}
		}
		tmp := filepath.Join(f.repoDir, ".git", "test.bundle")
		args := []string{"bundle", "create", tmp, "refs/heads/" + f.branch}
		if !full {
			args = append(args, "^"+req.Base)
		}
		if _, err := gitOut(args...); err != nil {
			p.done <- err
			return
		}
		data, err := os.ReadFile(tmp)
		_ = os.Remove(tmp)
		if err != nil {
			p.done <- err
			return
		}
		hdr := api.PullBundleHeader{
			Repo: &api.RepoInfo{Root: f.repoDir, Main: f.repoDir, Branch: f.branch},
			Head: head,
			Full: full,
			Size: int64(len(data)),
		}
		b, _ := json.Marshal(hdr)
		_, _ = outW.Write(append(b, '\n'))
		_, _ = outW.Write(data)
		p.done <- nil
	}()
	return p, nil
}

func TestPullBundleQuarantineReviewAndFallback(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	gitc := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(context.Background(), "git", append([]string{"-C", dir}, args...)...)
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
	remote := t.TempDir()
	local := t.TempDir()
	gitc(remote, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(remote, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitc(remote, "add", "a.txt")
	gitc(remote, "commit", "-q", "-m", "base")
	gitc(local, "clone", "-q", remote, ".")

	// Commit on feature branch plus an unrequested tag (tag must never cross).
	gitc(remote, "checkout", "-q", "-b", "feature")
	if err := os.WriteFile(filepath.Join(remote, "b.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitc(remote, "add", "b.txt")
	gitc(remote, "commit", "-q", "-m", "clean work")
	gitc(remote, "tag", "sneaky-tag")

	tr := &fakeBundleTransport{repoDir: remote, branch: "feature"}
	h := Host{Name: "box"}
	s := &Session{ID: "abcd2345", Name: "feat"}

	res, err := PullBundleOn(context.Background(), tr, h, s, PullBundleOptions{LocalRepo: local})
	if err != nil {
		t.Fatalf("PullBundleOn clean: %v", err)
	}
	if res.Branch != "feature" || res.Into != "feature" || res.Ahead != "1" {
		t.Fatalf("res = %+v", res)
	}
	// Working tree on laptop did not check out b.txt, and tag did not cross.
	if _, err := os.Stat(filepath.Join(local, "b.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("pull checked out files into working tree!")
	}
	if tags := gitc(local, "tag", "-l"); tags != "" {
		t.Errorf("tags leaked into laptop repo: %q", tags)
	}
	if msg := gitc(local, "log", "-1", "--format=%s", "feature"); msg != "clean work" {
		t.Errorf("feature head = %q", msg)
	}

	// Add a flagged CI workflow file: pull must stop before moving branch until AllowFlagged.
	if err := os.MkdirAll(filepath.Join(remote, ".github", "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, ".github", "workflows", "ci.yml"), []byte("name: ci\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitc(remote, "add", ".")
	gitc(remote, "commit", "-q", "-m", "add workflow")

	_, err = PullBundleOn(context.Background(), tr, h, s, PullBundleOptions{LocalRepo: local})
	var ue *UserError
	if !errors.As(err, &ue) || ue.Code != "pull_flagged" {
		t.Fatalf("flagged pull = %v, want pull_flagged", err)
	}
	if !strings.Contains(ue.Hint, "git diff ") || !strings.Contains(ue.Hint, "pull box/abcd2345 --allow-flagged") {
		t.Errorf("pull_flagged hint = %q, want git diff and --allow-flagged command", ue.Hint)
	}
	// Branch did not move yet.
	if msg := gitc(local, "log", "-1", "--format=%s", "feature"); msg != "clean work" {
		t.Errorf("feature moved despite flag refusal: %q", msg)
	}
	// With AllowFlagged, it moves.
	if _, err := PullBundleOn(context.Background(), tr, h, s, PullBundleOptions{LocalRepo: local, AllowFlagged: true}); err != nil {
		t.Fatalf("flagged pull with AllowFlagged: %v", err)
	}
	if msg := gitc(local, "log", "-1", "--format=%s", "feature"); msg != "add workflow" {
		t.Errorf("feature after AllowFlagged = %q", msg)
	}

	// Secret value in added lines triggers pull_flagged without leaking the secret.
	const secretVal = "sk-live-secret-1234567890"
	if err := os.WriteFile(filepath.Join(remote, "secret.txt"), []byte("val="+secretVal+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitc(remote, "add", "secret.txt")
	gitc(remote, "commit", "-q", "-m", "leak secret")
	_, err = PullBundleOn(context.Background(), tr, h, s, PullBundleOptions{
		LocalRepo: local,
		Secrets:   map[string]string{"MY_SECRET": secretVal},
	})
	if !errors.As(err, &ue) || ue.Code != "pull_flagged" {
		t.Fatalf("secret leak pull = %v, want pull_flagged", err)
	}
	if strings.Contains(ue.Msg, secretVal) || strings.Contains(ue.Hint, secretVal) {
		t.Fatalf("pull error leaked secret value: msg=%q hint=%q", ue.Msg, ue.Hint)
	}
	if !strings.Contains(ue.Msg, "MY_SECRET") {
		t.Errorf("pull error should name secret key MY_SECRET, got %q", ue.Msg)
	}

	// .gitmodules with CR is hard-blocked even with AllowFlagged.
	if err := os.WriteFile(filepath.Join(remote, ".gitmodules"), []byte("[submodule \"x\"]\r\n\tpath = x\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitc(remote, "add", ".gitmodules")
	gitc(remote, "commit", "-q", "-m", "bad gitmodules")
	if _, err := PullBundleOn(context.Background(), tr, h, s, PullBundleOptions{LocalRepo: local, AllowFlagged: true}); !errors.As(err, &ue) || ue.Code != "pull_flagged" {
		t.Fatalf("gitmodules CR = %v, want hard pull_flagged", err)
	}

	// Invalid --into branch names are rejected before git fetch/rev-list.
	for _, badInto := range []string{"-flag", "a:b", "bad..branch"} {
		if _, err := PullBundleOn(context.Background(), tr, h, s, PullBundleOptions{LocalRepo: local, Into: badInto}); !errors.As(err, &ue) || ue.Code != "bad_branch" {
			t.Fatalf("Into=%q => %v, want bad_branch", badInto, err)
		}
	}
	_ = bytes.Buffer{}
}

type scriptedBundleTransport struct {
	replies []struct {
		hdr  api.PullBundleHeader
		body []byte
	}
	idx int
}

func (s *scriptedBundleTransport) Run(context.Context, string, io.Reader) ([]byte, error) {
	return nil, errors.New("unused")
}
func (s *scriptedBundleTransport) ReachHint() string { return "check host" }
func (s *scriptedBundleTransport) Start(_ context.Context, _ string) (Proc, error) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	p := &bundleProc{inR: inR, inW: inW, outR: outR, outW: outW, done: make(chan error, 1)}
	rep := s.replies[s.idx]
	s.idx++
	go func() {
		defer outW.Close()
		_, _ = io.ReadAll(inR)
		b, _ := json.Marshal(rep.hdr)
		_, _ = outW.Write(append(b, '\n'))
		if len(rep.body) > 0 {
			_, _ = outW.Write(rep.body)
		}
		p.done <- nil
	}()
	return p, nil
}

func TestPullBundleValidatesRemoteBranchAndFullRetryEdgeCases(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	local := t.TempDir()
	cmd := exec.CommandContext(context.Background(), "git", "-C", local, "init", "-q", "-b", "main")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	h := Host{Name: "box"}
	s := &Session{ID: "abcd2345", Name: "feat"}
	var ue *UserError

	// 1. Crafted remote branch name is rejected with protocol error.
	for _, badBranch := range []string{"-flag", "a:b", "bad\nname"} {
		st := &scriptedBundleTransport{replies: []struct {
			hdr  api.PullBundleHeader
			body []byte
		}{
			{hdr: api.PullBundleHeader{Repo: &api.RepoInfo{Branch: badBranch}, Empty: true}},
		}}
		if _, err := PullBundleOn(context.Background(), st, h, s, PullBundleOptions{LocalRepo: local}); !errors.As(err, &ue) || ue.Code != "protocol" {
			t.Fatalf("remote branch %q => %v, want protocol error", badBranch, err)
		}
	}

	// 2. Full-bundle retry returning Empty=true succeeds with NothingNew instead of failing verify on "".
	stEmpty := &scriptedBundleTransport{replies: []struct {
		hdr  api.PullBundleHeader
		body []byte
	}{
		{hdr: api.PullBundleHeader{Repo: &api.RepoInfo{Branch: "feat"}, Full: false, Size: 5}, body: []byte("bad01")},
		{hdr: api.PullBundleHeader{Repo: &api.RepoInfo{Branch: "feat"}, Full: true, Empty: true}},
	}}
	res, err := PullBundleOn(context.Background(), stEmpty, h, s, PullBundleOptions{LocalRepo: local})
	if err != nil || !res.NothingNew {
		t.Fatalf("full retry empty => (%+v, %v), want NothingNew=true", res, err)
	}

	// 3. Full-bundle retry returning Repo=nil returns protocol error instead of panicking.
	stNil := &scriptedBundleTransport{replies: []struct {
		hdr  api.PullBundleHeader
		body []byte
	}{
		{hdr: api.PullBundleHeader{Repo: &api.RepoInfo{Branch: "feat"}, Full: false, Size: 5}, body: []byte("bad01")},
		{hdr: api.PullBundleHeader{Repo: nil, Full: true, Size: 5}, body: []byte("bad02")},
	}}
	if _, err := PullBundleOn(context.Background(), stNil, h, s, PullBundleOptions{LocalRepo: local}); !errors.As(err, &ue) || ue.Code != "protocol" {
		t.Fatalf("full retry nil repo => %v, want protocol error", err)
	}
}

func TestCheckPullMax(t *testing.T) {
	if got, canon, err := CheckPullMax(""); err != nil || got != DefaultPullMaxBytes || canon != api.DefaultPullMax {
		t.Errorf("CheckPullMax(\"\") = (%d, %q, %v), want (%d, %q, nil)", got, canon, err, DefaultPullMaxBytes, api.DefaultPullMax)
	}
	if got, _, err := CheckPullMax("1G"); err != nil || got != 1<<30 {
		t.Errorf("CheckPullMax(\"1G\") = (%d, %v), want (%d, nil)", got, err, int64(1<<30))
	}
	for _, bad := range []string{"0", "512", "nope", "-1M"} {
		var ue *UserError
		if _, _, err := CheckPullMax(bad); !errors.As(err, &ue) || ue.Code != "pull_max_invalid" || ue.Kind != KindUsage {
			t.Errorf("CheckPullMax(%q) = %v, want UserError{Code:pull_max_invalid, Kind:KindUsage}", bad, err)
		}
	}
}
