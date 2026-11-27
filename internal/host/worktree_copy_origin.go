package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

// The original add's actual native creation record constrains storage writes
// too. It is never converted to a runtime, start or stopped-session source.
func copyOriginalWorktreeIncludeV1(git *GitInfo, id string) (skipped []skippedCopy) {
	refuse := func(err error) []skippedCopy {
		return []skippedCopy{{includeFile, "the original worktree cannot be copied safely: " + err.Error()}}
	}
	if git == nil || len(git.extra) != 0 || !validID(id) {
		return refuse(placementError("the include copy has no original worktree add"))
	}
	publisher, err := beginStateMetadataPublisher(context.Background())
	if err != nil {
		return refuse(err)
	}
	defer func() {
		if err := publisher.close(); err != nil {
			skipped = append(skipped, skippedCopy{includeFile, "the original copy publisher did not close cleanly: " + err.Error()})
		}
	}()
	var origin worktreeOriginV1
	err = withFootprintControlV1(publisher, func(control *placementControl, _ *os.Root) error {
		var exists bool
		origin, exists = control.WorktreeOrigins[id]
		if !exists || origin.Phase != "installed" || origin.Repository != git.RepoRoot || filepath.Join(WorktreesDir(), origin.Relative) != git.Worktree || origin.Branch != git.Branch || origin.BaseSHA != git.BaseSHA {
			return placementError("the include copy lost its original native creation record")
		}
		return validateWorktreeOriginsV1(map[string]worktreeOriginV1{id: origin})
	})
	if err != nil {
		return refuse(err)
	}
	source, err := cachefs.OpenSafeAncestor(origin.Repository)
	if err != nil {
		return refuse(err)
	}
	defer source.Close()
	target, err := cachefs.OpenPrivateRoot(git.Worktree, false)
	if err != nil {
		return refuse(err)
	}
	defer target.Close()
	check := func() error {
		if err := errors.Join(publisher.ctx.Err(), origin.RepoRef.matchesRoot(source), origin.Directory.matchesRoot(target)); err != nil {
			return err
		}
		for _, pair := range []struct {
			path string
			ref  purgeRef
		}{{origin.Repository, origin.RepoRef}, {git.Worktree, origin.Directory}} {
			current, err := cachefs.OpenSafeAncestor(pair.path)
			if err != nil {
				return err
			}
			checked := pair.ref.matchesRoot(current)
			closed := current.Close()
			if checked != nil || closed != nil {
				return errors.Join(checked, closed)
			}
		}
		return nil
	}
	if err := check(); err != nil {
		return refuse(err)
	}
	patterns, why := includePatterns(source)
	if why != nil {
		return []skippedCopy{*why}
	}
	skipped = copyWorktreeIncludePatternsV1(source, target, patterns, check)
	if err := check(); err != nil {
		skipped = append(skipped, skippedCopy{includeFile, "the original worktree changed during copying: " + err.Error()})
	}
	return skipped
}
