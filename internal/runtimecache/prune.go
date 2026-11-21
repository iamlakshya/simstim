package runtimecache

import (
	"context"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/iamlakshya/simstim/internal/supply"
)

// References names the runtime label and direct artefact digests recorded by
// one session that has not been deleted. Stopped sessions retain files too.
type References struct {
	Label   string
	Digests []string
}

// Prune keeps two newest runtime labels, every session reference and files
// younger than one hour. dryRun returns the same candidates without removing
// them. The scanner and deletion share the store's interprocess lock.
func (s *Store) Prune(ctx context.Context, arch string, sessions []References, now time.Time, dryRun bool) ([]string, error) {
	return s.PruneWithReferences(ctx, arch, func() ([]References, error) { return sessions, nil }, now, dryRun)
}

// PruneWithReferences reads the current session references while holding the
// same transaction used by publication and deletion. Future runtime start
// writers must retain their references in this transaction too.
func (s *Store) PruneWithReferences(ctx context.Context, arch string, readReferences func() ([]References, error), now time.Time, dryRun bool) ([]string, error) {
	if s.readOnly && !dryRun {
		return nil, fmt.Errorf("cannot prune a read-only runtime store")
	}
	var removed []string
	err := retryExecMode(ctx, func() error {
		removed = nil
		return s.withPublication(ctx, func() error {
			return s.locked(ctx, func() error {
				records, err := s.recordsContext(ctx, arch)
				if err != nil {
					return err
				}
				items := map[string]supply.Item{}
				for _, record := range records {
					for _, item := range record.Manifest.Items {
						items[item.SHA256] = item
					}
				}
				sessions, err := readReferences()
				if err != nil {
					return err
				}
				labels := map[string]bool{}
				keep := map[string]bool{}
				for _, session := range sessions {
					if session.Label != "" && !ValidLabel(session.Label) {
						return fmt.Errorf("session runtime label is invalid")
					}
					if session.Label != "" {
						labels[session.Label] = true
					}
					for _, digest := range session.Digests {
						if !supply.IsHex64(digest) {
							return fmt.Errorf("session runtime digest is invalid")
						}
						keep[digest] = true
					}
				}
				for i, record := range records {
					if i < 2 || labels[record.Manifest.Runtime] {
						delete(labels, record.Manifest.Runtime)
						for _, it := range record.Manifest.Items {
							keep[it.SHA256] = true
						}
					}
				}
				if len(labels) > 0 {
					return fmt.Errorf("a session's runtime record is missing")
				}
				dir, err := s.root.Open("sha256")
				if err != nil {
					return fmt.Errorf("open runtime files for prune: %w", err)
				}
				defer dir.Close()
				entries, err := dir.ReadDir(-1)
				if err != nil {
					return fmt.Errorf("list runtime files for prune: %w", err)
				}
				type candidate struct {
					name string
					info os.FileInfo
				}
				var candidates []candidate
				for _, entry := range entries {
					if err := ctx.Err(); err != nil {
						return err
					}
					if !supply.IsHex64(entry.Name()) {
						return fmt.Errorf("runtime store contains an unknown file")
					}
					item, known := items[entry.Name()]
					if err := s.checkPruneModeIntent(entry.Name(), known); err != nil {
						return err
					}
					if known {
						_, done, err := s.artifactModeGuard(ctx, item)
						if err != nil {
							return err
						}
						// Begin/creation also takes this global transaction briefly,
						// so no new mode phase can enter before this scan/removal ends.
						done()
					}
					info, err := s.root.Lstat("sha256/" + entry.Name())
					if err != nil {
						return fmt.Errorf("inspect runtime prune file: %w", err)
					}
					mode := info.Mode().Perm()
					if !info.Mode().IsRegular() || (mode != 0o444 && mode != 0o555) {
						return fmt.Errorf("runtime prune file is unsafe")
					}
					checked, err := s.openRegular("sha256/"+entry.Name(), mode)
					if err != nil {
						return err
					}
					checked.Close()
					if !keep[entry.Name()] && now.Sub(info.ModTime()) >= time.Hour {
						candidates = append(candidates, candidate{entry.Name(), info})
					}
				}
				for _, candidate := range candidates {
					if err := ctx.Err(); err != nil {
						return err
					}
					if !dryRun {
						if s.beforePruneRemove != nil {
							s.beforePruneRemove(candidate.name)
						}
						current, err := s.root.Lstat("sha256/" + candidate.name)
						if err != nil {
							return fmt.Errorf("recheck runtime prune file: %w", err)
						}
						if !os.SameFile(candidate.info, current) || !current.ModTime().Equal(candidate.info.ModTime()) {
							return fmt.Errorf("runtime file changed before prune")
						}
						if err := s.root.Remove("sha256/" + candidate.name); err != nil {
							return fmt.Errorf("remove old runtime file: %w", err)
						}
						if item, known := items[candidate.name]; known {
							if err := s.removeModeAfterPrune(ctx, item); err != nil {
								return err
							}
						}
					}
					removed = append(removed, candidate.name)
				}
				if !dryRun && len(removed) > 0 {
					return s.syncDir("sha256")
				}
				return nil
			})
		})
	})
	sort.Strings(removed)
	return removed, err
}
