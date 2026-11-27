package host

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/paths"
)

// Worktree origins come only from the actual native directory producer and
// the original Git ref transaction. GitInfo remains a description. Preparing
// a new ref lets this owner retain the newly created .lock descriptor BEFORE
// Git publishes it, then verify that SAME inode became the branch ref.
type worktreeOriginV1 struct {
	Relative   string           `json:"relative"`
	Repository string           `json:"repository"`
	Common     string           `json:"common"`
	Branch     string           `json:"branch"`
	BaseSHA    string           `json:"base_sha"`
	Directory  purgeRef         `json:"directory"`
	Birth      *worktreeBirthV2 `json:"native_birth,omitempty"`
	Parent     purgeRef         `json:"parent"`
	RepoRef    purgeRef         `json:"repository_root"`
	CommonRef  purgeRef         `json:"common_root"`
	BranchRef  *footprintRef    `json:"branch_ref,omitempty"`
	Phase      string           `json:"phase"`
	extra      map[string]json.RawMessage
}

func (origin *worktreeOriginV1) UnmarshalJSON(data []byte) error {
	type plain worktreeOriginV1
	var value plain
	extra, err := decodeFootprint(data, &value, []string{"relative", "repository", "common", "branch", "base_sha", "directory", "native_birth", "parent", "repository_root", "common_root", "branch_ref", "phase"})
	*origin = worktreeOriginV1(value)
	origin.extra = extra
	return err
}

func (origin worktreeOriginV1) MarshalJSON() ([]byte, error) {
	type plain worktreeOriginV1
	return encodeFootprint(plain(origin), origin.extra)
}

func validateWorktreeOriginsV1(origins map[string]worktreeOriginV1) error {
	for id, origin := range origins {
		if origin.Birth != nil && !origin.Birth.knownV2() {
			return placementError("the original worktree has an unknown native filesystem birth record")
		}
		parts := strings.Split(origin.Relative, string(filepath.Separator))
		if !validID(id) || len(origin.extra) != 0 || !validPurgeRelative([]byte(origin.Relative)) || len(parts) != 2 || parts[0] != filepath.Base(origin.Repository) || !strings.HasSuffix(parts[1], "-"+id) || origin.Branch != brand.Name+"/"+parts[1] || !validPurgeGitTip(origin.BaseSHA) || !filepath.IsAbs(origin.Repository) || filepath.Clean(origin.Repository) != origin.Repository || !filepath.IsAbs(origin.Common) || filepath.Clean(origin.Common) != origin.Common || !origin.Directory.validDirectory() || !origin.Parent.validDirectory() || !purgeSafeParentRefV1(origin.RepoRef) || !purgeSafeParentRefV1(origin.CommonRef) {
			return placementError("the original worktree creation receipt is unknown")
		}
		switch origin.Phase {
		case "directory":
			if origin.BranchRef != nil {
				return placementError("the worktree directory phase has an unexpected branch receipt")
			}
		case "branch-prepared", "branch-created", "installed":
			if origin.BranchRef == nil || len(origin.BranchRef.extra) != 0 || origin.BranchRef.Inode == 0 || origin.BranchRef.UID != uint64(os.Getuid()) || os.FileMode(origin.BranchRef.Mode)&^os.FileMode(0o777) != 0 || os.FileMode(origin.BranchRef.Mode)&0o022 != 0 || origin.BranchRef.Size != int64(len(origin.BaseSHA)+1) || origin.BranchRef.SHA256 != footprintDigest([]byte(origin.BaseSHA+"\n")) {
				return placementError("the original worktree branch lacks its native prepared ref")
			}
		default:
			return placementError("the original worktree creation has an unknown phase")
		}
	}
	return nil
}

type nativeWorktreeAddV1 struct {
	publisher  *stateMetadataPublisher
	id         string
	origin     worktreeOriginV1
	root       *os.Root
	parent     *os.Root
	repository *os.Root
	common     *os.Root
}

func beginNativeWorktreeAddV1(g *GitInfo, id string) (result *nativeWorktreeAddV1, returned error) {
	if g == nil || !validID(id) || !g.CreatedBy || !validPurgeGitTip(g.BaseSHA) {
		return nil, placementError("worktree creation has no original Git add request")
	}
	ctx := gitContext()
	publisher, err := beginStateMetadataPublisher(ctx)
	if err != nil {
		return nil, err
	}
	owner := &nativeWorktreeAddV1{publisher: publisher, id: id}
	defer func() {
		if result == nil {
			returned = errors.Join(returned, owner.close())
		}
	}()
	relative, err := filepath.Rel(WorktreesDir(), g.Worktree)
	if err != nil || !validPurgeRelative([]byte(relative)) || strings.Count(relative, string(filepath.Separator)) != 1 {
		return nil, placementError("the worktree creator received an unknown fixed storage role")
	}
	common, err := git(g.RepoRoot, "rev-parse", "--git-common-dir")
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(g.RepoRoot, common)
	}
	owner.repository, err = cachefs.OpenSafeAncestor(g.RepoRoot)
	if err != nil {
		return nil, err
	}
	owner.common, err = cachefs.OpenSafeAncestor(filepath.Clean(common))
	if err != nil {
		return nil, err
	}
	owner.origin = worktreeOriginV1{Relative: relative, Repository: g.RepoRoot, Common: filepath.Clean(common), Branch: g.Branch, BaseSHA: g.BaseSHA, Phase: "directory"}
	if owner.origin.RepoRef, err = nativeWorktreeDirectoryRefV1(owner.repository); err != nil {
		return nil, err
	}
	if owner.origin.CommonRef, err = nativeWorktreeDirectoryRefV1(owner.common); err != nil {
		return nil, err
	}
	err = withFootprintControlV1(publisher, func(control *placementControl, guard *os.Root) error {
		if _, exists := control.WorktreeOrigins[id]; exists {
			return placementError("another original worktree already owns this session identity")
		}
		owner.parent, err = cachefs.OpenPrivateRootCreated(filepath.Dir(g.Worktree), func(path string, info os.FileInfo) error {
			relative, err := filepath.Rel(paths.DataDir(), path)
			if err != nil || !validPurgeRelative([]byte(relative)) || relative == "." || !refOf(info).validDirectory() {
				return placementError("the worktree creator made an unknown storage parent")
			}
			if control.Refs == nil {
				control.Refs = map[string]purgeRef{}
			}
			control.Refs["directory:data/"+relative] = refOf(info)
			return writePlacementControl(guard, *control)
		})
		if err != nil {
			return err
		}
		if err := owner.parent.Mkdir(filepath.Base(g.Worktree), 0o700); err != nil {
			return err
		}
		info, err := owner.parent.Lstat(filepath.Base(g.Worktree))
		if err != nil || !refOf(info).validDirectory() {
			return placementError("the new worktree directory lost its native creation identity")
		}
		owner.origin.Directory = refOf(info)
		owner.root, err = cachefs.OpenPrivateChild(owner.parent, filepath.Base(g.Worktree), false)
		if err != nil {
			return err
		}
		owner.origin.Birth, err = captureWorktreeBirthV2(owner.root)
		if err != nil {
			return err
		}
		owner.origin.Parent, err = nativeWorktreeDirectoryRefV1(owner.parent)
		if err != nil {
			return err
		}
		if err := validateWorktreeOriginsV1(map[string]worktreeOriginV1{id: owner.origin}); err != nil {
			return err
		}
		if control.WorktreeOrigins == nil {
			control.WorktreeOrigins = map[string]worktreeOriginV1{}
		}
		control.WorktreeOrigins[id] = owner.origin
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := owner.createOriginalBranch(); err != nil {
		return nil, UserError("worktree", fmt.Sprintf("the original worktree %s was reserved but its branch creation did not finish: %v", g.Worktree, err), "Keep its original creation record and inspect the worktree and branch before retrying.")
	}
	return owner, nil
}

func nativeWorktreeDirectoryRefV1(root *os.Root) (purgeRef, error) {
	file, err := root.Open(".")
	if err != nil {
		return purgeRef{}, err
	}
	info, err := file.Stat()
	closed := file.Close()
	if err != nil || closed != nil {
		return purgeRef{}, errors.Join(err, closed)
	}
	return refOf(info), nil
}

func (owner *nativeWorktreeAddV1) check() error {
	if owner == nil || owner.publisher == nil || owner.root == nil || owner.parent == nil || owner.repository == nil || owner.common == nil {
		return placementError("the original worktree creation owner ended")
	}
	if owner.origin.Birth != nil {
		if err := checkWorktreeBirthV2(owner.root, owner.origin.Birth); err != nil {
			return err
		}
	}
	for _, pair := range []struct {
		ref  purgeRef
		root *os.Root
	}{{owner.origin.Directory, owner.root}, {owner.origin.Parent, owner.parent}, {owner.origin.RepoRef, owner.repository}, {owner.origin.CommonRef, owner.common}} {
		if err := pair.ref.matchesRoot(pair.root); err != nil {
			return err
		}
		current, err := cachefs.OpenSafeAncestor(pair.root.Name())
		if err != nil {
			return err
		}
		err = pair.ref.matchesRoot(current)
		closed := current.Close()
		if err != nil || closed != nil {
			return errors.Join(err, closed)
		}
	}
	return owner.publisher.ctx.Err()
}

func (owner *nativeWorktreeAddV1) publish() error {
	if err := owner.check(); err != nil {
		return err
	}
	if err := validateWorktreeOriginsV1(map[string]worktreeOriginV1{owner.id: owner.origin}); err != nil {
		return err
	}
	if owner.origin.BranchRef != nil {
		leaf := filepath.Join("refs", "heads", owner.origin.Branch)
		if owner.origin.Phase == "branch-prepared" {
			leaf += ".lock"
		}
		file, current, err := openNativeWorktreeBranchV1(owner.common, leaf)
		if err != nil {
			return err
		}
		closed := file.Close()
		if closed != nil || !footprintRefEqualV1(current, owner.origin.BranchRef) {
			return errors.Join(placementError("the original branch changed before its producer receipt"), closed)
		}
	}
	return withFootprintControlV1(owner.publisher, func(control *placementControl, _ *os.Root) error {
		prior, exists := control.WorktreeOrigins[owner.id]
		if !exists || !samePurgeDirectoryRef(prior.Directory, owner.origin.Directory) || !samePurgeDirectoryRef(prior.CommonRef, owner.origin.CommonRef) {
			return placementError("the worktree publication lost its original creation record")
		}
		control.WorktreeOrigins[owner.id] = owner.origin
		return nil
	})
}

func (owner *nativeWorktreeAddV1) createOriginalBranch() (returned error) {
	if err := owner.check(); err != nil {
		return err
	}
	// Reftable and packed/changed refs have no matching native loose-ref birth
	// here. Never turn a successful Git description into ownership of one.
	storage, code, err := runGit(owner.origin.Repository, nil, "config", "--get", "extensions.refStorage")
	if err != nil && code != 1 || storage != "" && storage != "files" {
		return placementError("this repository has no supported native loose-ref creation route")
	}
	hooks, err := configHooks(owner.origin.Repository)
	if err != nil {
		return err
	}
	cmd := gitCommand(gitArgs(owner.origin.Repository, hooks, "-c", "core.logAllRefUpdates=false", "update-ref", "--stdin")...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	input, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		return errors.Join(err, input.Close())
	}
	if err := cmd.Start(); err != nil {
		return errors.Join(err, input.Close(), output.Close())
	}
	joined, inputClosed := false, false
	// Closing this exact stdin aborts an uncommitted transaction. Join its
	// actual process before the live producer or namespace descriptors close.
	defer func() {
		if !inputClosed {
			returned = errors.Join(returned, input.Close())
		}
		if !joined {
			waitErr := cmd.Wait()
			if waitErr != nil {
				returned = errors.Join(returned, fmt.Errorf("original branch transaction failed: %w: %s", waitErr, firstLine(stderr.String())))
			}
		}
	}()
	reader := bufio.NewReaderSize(output, 4096)
	step := func(command, expected string) error {
		if _, err := io.WriteString(input, command+"\n"); err != nil {
			return err
		}
		line, err := reader.ReadSlice('\n')
		if err != nil || string(line) != expected+"\n" {
			return errors.Join(placementError("the original Git branch transaction supplied an unknown continuation"), err)
		}
		return nil
	}
	if err := step("start", "start: ok"); err != nil {
		return err
	}
	if _, err := io.WriteString(input, "create refs/heads/"+owner.origin.Branch+" "+owner.origin.BaseSHA+"\n"); err != nil {
		return err
	}
	if err := step("prepare", "prepare: ok"); err != nil {
		return err
	}
	leaf := filepath.Join("refs", "heads", owner.origin.Branch)
	parent, err := cachefs.OpenSafeAncestor(filepath.Join(owner.common.Name(), filepath.Dir(leaf)))
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, parent.Close()) }()
	file, prepared, err := openNativeWorktreeBranchV1(parent, filepath.Base(leaf)+".lock")
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, file.Close()) }()
	owner.origin.BranchRef, owner.origin.Phase = prepared, "branch-prepared"
	if err := owner.publish(); err != nil {
		return err
	}
	if err := step("commit", "commit: ok"); err != nil {
		return err
	}
	info, err := cachefs.ValidateOwnedFile(parent, filepath.Base(leaf), file, os.FileMode(prepared.Mode))
	if err != nil || !footprintRefMatchesV1(prepared, info) {
		return placementError("the original prepared branch did not retain its native publication identity")
	}
	current, err := nativeWorktreeBranchRefV1(file, os.FileMode(prepared.Mode))
	if err != nil || !footprintRefEqualV1(current, prepared) {
		return placementError("the original prepared branch changed during publication")
	}
	inputClosed = true
	if err := input.Close(); err != nil {
		return err
	}
	waitErr := cmd.Wait()
	joined = true
	if waitErr != nil {
		return fmt.Errorf("join the original branch creation: %w: %s", waitErr, firstLine(stderr.String()))
	}
	owner.origin.Phase = "branch-created"
	return owner.publish()
}

func (owner *nativeWorktreeAddV1) finish() error {
	if err := owner.check(); err != nil {
		return err
	}
	file, ref, err := openNativeWorktreeBranchV1(owner.common, filepath.Join("refs", "heads", owner.origin.Branch))
	if err != nil {
		return err
	}
	closeErr := file.Close()
	if closeErr != nil || !footprintRefEqualV1(ref, owner.origin.BranchRef) {
		return errors.Join(placementError("the worktree checkout changed the original native branch"), closeErr)
	}
	owner.origin.Phase = "installed"
	return owner.publish()
}

func openNativeWorktreeBranchV1(root *os.Root, relative string) (*os.File, *footprintRef, error) {
	before, err := root.Lstat(relative)
	if err != nil {
		return nil, nil, err
	}
	if !before.Mode().IsRegular() || before.Mode().Perm()&0o022 != 0 || !wallCacheOwned(before) {
		return nil, nil, placementError("the original branch ref has an unsafe native type, owner or mode")
	}
	file, err := root.Open(relative)
	if err != nil {
		return nil, nil, err
	}
	ref, err := nativeWorktreeBranchRefV1(file, before.Mode().Perm())
	if err != nil || !footprintRefMatchesV1(ref, before) {
		_ = file.Close()
		return nil, nil, errors.Join(placementError("the original branch ref changed during capture"), err)
	}
	return file, ref, nil
}

func nativeWorktreeBranchRefV1(file *os.File, mode os.FileMode) (*footprintRef, error) {
	before, err := cachefs.ValidateDescriptor(file, mode)
	if err != nil {
		return nil, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(file, 66))
	if err != nil || len(raw) > 65 {
		return nil, errors.Join(placementError("the original branch ref exceeds its fixed object bound"), err)
	}
	after, err := cachefs.ValidateDescriptor(file, mode)
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return nil, placementError("the original branch ref changed while its contents were read")
	}
	device, inode, uid, _, ok := footprintIdentity(before)
	if !ok {
		return nil, placementError("the original branch ref has no native identity")
	}
	return &footprintRef{Device: device, Inode: inode, UID: uid, Mode: uint32(before.Mode()), Size: before.Size(), ModTime: before.ModTime(), SHA256: footprintDigest(raw)}, nil
}

func (owner *nativeWorktreeAddV1) close() error {
	if owner == nil {
		return nil
	}
	var returned error
	for _, root := range []*os.Root{owner.root, owner.parent, owner.repository, owner.common} {
		if root != nil {
			returned = errors.Join(returned, root.Close())
		}
	}
	if owner.publisher != nil {
		returned = errors.Join(returned, owner.publisher.close())
	}
	owner.publisher, owner.root, owner.parent, owner.repository, owner.common = nil, nil, nil, nil, nil
	return returned
}
