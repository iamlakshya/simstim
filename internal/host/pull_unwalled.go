package host

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/pullexport"
)

type unwalledPullSource struct {
	id, cwd string
	limit   int64
	close   func() error
	mu      sync.Mutex
	paths   []string
	once    sync.Once
	err     error
}

// NewUnwalledPullSource exports an ordinary non-SSH session through the
// canonical scoped git-op. The canonical primary owner supplies its already
// held lease release. A sandbox record can never enter this host-Git path.
func NewUnwalledPullSource(m *Manifest, release func() error) (pullexport.Source, error) {
	if m == nil || !api.ValidSessionID(m.ID) || !filepath.IsAbs(m.Cwd) || m.Wall != nil || m.SandboxOf != "" || release == nil {
		return nil, UserError("pull_source", "the scoped pull source is not an unwalled primary", "Use the sandbox's own bundle exporter.")
	}
	limit, err := EffectivePullMax(m)
	if err != nil {
		return nil, err
	}
	return &unwalledPullSource{id: m.ID, cwd: m.Cwd, limit: limit, close: release}, nil
}

// SelectRepository accepts only this already owned ordinary repository.
func (s *unwalledPullSource) SelectRepository(name string) error {
	if name != "" && (!api.InputRepoName(name) || name != filepath.Base(s.cwd)) {
		return UserError("pull_repo", "the unwalled source has no such protected repository", "Choose its original repository basename, or omit --repo.")
	}
	return nil
}

func (s *unwalledPullSource) SeedHeads() []string { return nil }
func (s *unwalledPullSource) PullMax() int64      { return s.limit }

func (s *unwalledPullSource) Capture(ctx context.Context, args pullexport.CaptureArgs, writer io.Writer) (pullexport.CaptureResult, error) {
	directory, err := pullSpoolDir(s.id)
	if err != nil {
		return pullexport.CaptureResult{}, err
	}
	directory, err = os.MkdirTemp(directory, "unwalled-")
	if err != nil {
		return pullexport.CaptureResult{}, err
	}
	s.mu.Lock()
	s.paths = append(s.paths, directory)
	s.mu.Unlock()
	scratch, err := os.MkdirTemp("", brand.Name+"-pull-stage-")
	if err != nil {
		return pullexport.CaptureResult{}, err
	}
	s.mu.Lock()
	s.paths = append(s.paths, scratch)
	s.mu.Unlock()
	request := &gitPullArgs{Dir: s.cwd, ID: s.id, Output: directory, Scratch: scratch, Limit: s.limit}
	for _, head := range args.Heads {
		request.Heads = append(request.Heads, gitPullHead{Name: head.Name, OID: head.OID})
	}
	kind := findGitOp(gitOpPullBundle)
	answer, err := gitOpWithinContext(ctx, kind, gitOpRequest{Pull: request}, kind.deadline)
	if err != nil {
		return pullexport.CaptureResult{}, err
	}
	if answer.Pull == nil || !api.ValidGitOID(answer.Pull.OID) {
		return pullexport.CaptureResult{}, UserError("pull_protocol", "the scoped bundle has no valid result", "Check the session repository and try again.")
	}
	// The child chooses a name only beneath this protected output root.
	// os.Root keeps an unexpected result path from granting another read.
	root, err := os.OpenRoot(directory)
	if err != nil {
		return pullexport.CaptureResult{}, err
	}
	defer root.Close()
	relative, err := filepath.Rel(directory, answer.Pull.Path)
	if err != nil || filepath.IsAbs(relative) || !filepath.IsLocal(relative) {
		return pullexport.CaptureResult{}, UserError("pull_protocol", "the scoped bundle left its owned output root", "Reinstall the host helper and try again.")
	}
	file, err := root.Open(relative)
	if err != nil {
		return pullexport.CaptureResult{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > s.limit {
		return pullexport.CaptureResult{}, UserError("pull_protocol", "the scoped bundle is not a bounded regular file", "Check the session storage and try again.")
	}
	if _, err = io.Copy(writer, io.LimitReader(file, s.limit+1)); err != nil {
		return pullexport.CaptureResult{}, err
	}
	return pullexport.CaptureResult{DefaultRef: answer.Pull.Ref, Full: true}, nil
}

func (s *unwalledPullSource) RecordPulled(ctx context.Context, commits []string) error {
	return RecordPulledUnderLease(ctx, s.id, commits)
}

func (s *unwalledPullSource) Close() error {
	s.once.Do(func() {
		s.mu.Lock()
		paths := slices.Clone(s.paths)
		s.mu.Unlock()
		for _, path := range paths {
			_ = os.RemoveAll(path)
		}
		s.err = s.close()
	})
	return s.err
}

// RecordPulledUnderLease saves only validated identities after the canonical
// owner obtained the primary lease. A live supervisor must instead merge into
// its own current manifest and use its canonical fresh-save path.
func RecordPulledUnderLease(ctx context.Context, id string, commits []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, oid := range commits {
		if !api.ValidGitOID(oid) {
			return UserError("pull_protocol", "the fetched commit identity is invalid", "Repeat pull through the current client.")
		}
	}
	m, err := LoadManifest(id)
	if err != nil {
		return fmt.Errorf("reload the pulled session: %w", err)
	}
	if m.Wall == nil && SessionRunning(m) {
		_, err := HolderRequest(id, "pull.record", holderPulledArgs{ID: id, Commits: slices.Clone(commits)}, 5*time.Second)
		return err // never replace a live holder's failed ack with a disk save
	}
	if err = ApplyPulled(m, commits); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	return m.Save()
}
