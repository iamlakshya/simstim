package guest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/gitpolicy"
	"github.com/iamlakshya/simstim/internal/proto"
)

func snapshotGitErrorV1(message string) error {
	return &proto.Error{Code: "fork_refs", Message: message, Hint: "Check the sandbox's session branches and retry before starting its agent."}
}

func (g *Init) snapshotRefsV1(raw json.RawMessage) (proto.CtlRefsResultV1, error) {
	var args proto.CtlRefsArgsV1
	if !api.ExactJSONFields(raw, args) || json.Unmarshal(raw, &args) != nil || len(args.Pulled) > 4096 {
		return proto.CtlRefsResultV1{}, snapshotGitErrorV1("the refs request is invalid")
	}
	for _, oid := range args.Pulled {
		if !api.ValidGitOID(oid) {
			return proto.CtlRefsResultV1{}, snapshotGitErrorV1("the pulled commit identity is invalid")
		}
	}
	g.mu.Lock()
	cwd, primary := g.execCwd, g.primary
	g.mu.Unlock()
	if !api.ValidSessionID(primary) || cwd != "/work" && !strings.HasPrefix(cwd, "/work/") {
		return proto.CtlRefsResultV1{}, snapshotGitErrorV1("the running guest has no session workspace")
	}
	workspace, err := os.OpenRoot(g.path(cwd))
	if err != nil {
		return proto.CtlRefsResultV1{}, err
	}
	git, statErr := workspace.Lstat(".git")
	closeErr := workspace.Close()
	if closeErr != nil {
		return proto.CtlRefsResultV1{}, closeErr
	}
	if errors.Is(statErr, os.ErrNotExist) {
		return proto.CtlRefsResultV1{Refs: []proto.BundleRef{}}, nil
	}
	if statErr != nil {
		return proto.CtlRefsResultV1{}, statErr
	}
	if !git.IsDir() || git.Mode()&os.ModeSymlink != 0 {
		return proto.CtlRefsResultV1{}, snapshotGitErrorV1("the guest workspace has an indirect Git directory")
	}
	ctx, cancel := context.WithTimeout(g.lifetime, 5*time.Second)
	defer cancel()
	release, err := g.holdCommandOperation()
	if err != nil {
		return proto.CtlRefsResultV1{}, err
	}
	defer release()
	op := &bundleOperation{ctx: ctx, args: proto.CtlBundleArgs{Cwd: cwd, Primary: primary}}
	prefix := "refs/heads/" + brand.Name + "/" + primary + "/"
	data, err := g.bundleProbe(op, cwd, "for-each-ref", "--format=%(objectname) %(refname)", prefix)
	if err != nil {
		return proto.CtlRefsResultV1{}, err
	}
	refs, err := parseBundleHeads(data, prefix)
	if err != nil || len(refs) > 4096 {
		return proto.CtlRefsResultV1{}, snapshotGitErrorV1("the session refs exceed the checkpoint metadata bound")
	}
	if refs == nil {
		refs = []proto.BundleRef{}
	}
	status, err := g.bundleProbe(op, cwd, "status", "--porcelain=v1", "--untracked-files=normal")
	return proto.CtlRefsResultV1{Refs: refs, Dirty: status != ""}, err
}

// renameForkBranchesV1 executes only in the new guest, before any holder.
// Each ref moves using its expected oid, and HEAD follows the renamed ref.
// Git runs through the same owned command engine used by pull, as agent.
func (g *Init) renameForkBranchesV1(parent, child string) error {
	if !api.ValidSessionID(parent) || !api.ValidSessionID(child) || parent == child {
		return snapshotGitErrorV1("the fork branch identities are invalid")
	}
	g.mu.Lock()
	busy := g.ended || g.holder != nil || len(g.members) > 0 || g.starting && g.primary != child
	for id := range g.memberStarts {
		busy = busy || id != child
	}
	g.mu.Unlock()
	if busy {
		return snapshotGitErrorV1("fork branches must be renamed before the first agent starts")
	}
	release, err := g.holdCommandOperation()
	if err != nil {
		return err
	}
	defer release()
	work, err := os.OpenRoot(g.path("/work"))
	if err != nil {
		return err
	}
	defer work.Close()
	directory, err := work.Open(".")
	if err != nil {
		return err
	}
	entries, readErr := directory.ReadDir(257)
	closeErr := directory.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) || closeErr != nil {
		return errors.Join(readErr, closeErr)
	}
	if len(entries) > 256 {
		return snapshotGitErrorV1("the fork has too many workspace repositories")
	}
	ctx, cancel := context.WithTimeout(g.lifetime, 30*time.Second)
	defer cancel()
	oldPrefix := "refs/heads/" + brand.Name + "/" + parent + "/"
	newPrefix := "refs/heads/" + brand.Name + "/" + child + "/"
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		git, statErr := work.Lstat(name + "/.git")
		if errors.Is(statErr, os.ErrNotExist) {
			continue
		}
		if statErr != nil {
			return statErr
		}
		if !git.IsDir() || git.Mode()&os.ModeSymlink != 0 {
			return snapshotGitErrorV1("a fork repository has an indirect Git directory")
		}
		cwd := filepath.ToSlash(filepath.Join("/work", name))
		op := &bundleOperation{ctx: ctx, args: proto.CtlBundleArgs{Cwd: cwd, Primary: parent}}
		version, err := g.bundleProbe(op, cwd, "--version")
		if err != nil {
			return err
		}
		if _, safe := gitpolicy.HookVersion(version); !safe {
			return snapshotGitErrorV1("the guest Git cannot disable every hook during branch rename")
		}
		data, err := g.bundleProbe(op, cwd, "for-each-ref", "--format=%(objectname) %(refname)", oldPrefix)
		if err != nil {
			return err
		}
		refs, err := parseBundleHeads(data, oldPrefix)
		if err != nil {
			return err
		}
		if len(refs) > 4096 {
			return snapshotGitErrorV1(fmt.Sprintf("the fork repository %s has too many session branches", name))
		}
		for _, ref := range refs {
			next := newPrefix + strings.TrimPrefix(ref.Name, oldPrefix)
			if _, err := g.bundleProbe(op, cwd, "check-ref-format", next); err != nil {
				return err
			}
			// Git's non-forced rename refuses an existing destination and moves
			// the reflog and symbolic HEAD with the branch, under its ref locks.
			if _, err := g.bundleProbe(op, cwd, "branch", "-m", "--", strings.TrimPrefix(ref.Name, "refs/heads/"), strings.TrimPrefix(next, "refs/heads/")); err != nil {
				return err
			}
		}
	}
	return nil
}
