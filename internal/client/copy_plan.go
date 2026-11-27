package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/files"
)

type copyRoot struct {
	name string
	info os.FileInfo
}

type plannedCopy struct {
	root    copyRoot
	name    string
	info    os.FileInfo
	file    api.InputFile
	folders []privateFolder
	include bool // set only by the reviewed local primary include constructor
}

type copyPlan struct {
	files []plannedCopy
	bytes int64
}

func copyInputError(code, message string) error {
	return &UserError{Code: code, Msg: message, Hint: "Choose ordinary non-sensitive files, or use --include for an explicit repository file.", Kind: KindRefused}
}

func (r copyRoot) open() (*os.Root, error) {
	root, err := os.OpenRoot(r.name)
	if err != nil {
		return nil, err
	}
	actual, err := root.Stat(".")
	if err != nil || !os.SameFile(r.info, actual) {
		root.Close()
		return nil, copyInputError("copy_changed", "the local copy root changed after planning")
	}
	return root, nil
}

// checkCopyPath reuses the paste policy on typed and resolved names and folder
// identities. The rooted descriptor and a second no-follow policy open must
// name the same object. No path-only check authorises bytes for publication.
func checkCopyPath(root *os.Root, name, typed string, file *os.File, folders []privateFolder) error {
	resolved, err := filepath.EvalSymlinks(filepath.Join(root.Name(), filepath.FromSlash(name)))
	if err != nil {
		return err
	}
	for _, candidate := range []string{typed, resolved} {
		if privateName(filepath.Base(candidate)) != "" || inPrivateFolder(candidate, folders) != "" {
			return copyInputError("copy_sensitive", "a copied path has a sensitive name or lies in a private folder")
		}
	}
	actual, err := file.Stat()
	if err != nil {
		return err
	}
	for _, folder := range folders {
		if os.SameFile(actual, folder.info) {
			return copyInputError("copy_sensitive", "a copied folder aliases a private folder")
		}
	}
	want, err := os.Stat(resolved)
	if err != nil || !os.SameFile(actual, want) {
		return copyInputError("copy_changed", "a copied path changed while checking its resolved name")
	}
	if actual.Mode().IsRegular() {
		checked, why := openResolved(resolved, folders)
		if why != "" {
			return copyInputError("copy_sensitive", "a copied file failed the sensitive-path descriptor check")
		}
		defer checked.Close()
		checkedInfo, err := checked.Stat()
		if err != nil || !os.SameFile(actual, checkedInfo) {
			return copyInputError("copy_changed", "a copied file changed during the descriptor check")
		}
	}
	return nil
}

func buildCopyPlan(ctx context.Context, sources []CopySource, home string, limit int64) (*copyPlan, error) {
	if limit <= 0 {
		return nil, copyInputError("bad_copy_max", "copy_max must be a positive size")
	}
	plan := &copyPlan{}
	targets := map[string]string{}
	folders := privateFolderInfos()
	for _, source := range sources {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := checkCopySpelling(source.Host, folders); err != nil {
			return nil, err
		}
		parent, name, defaultSource, err := copySourceParts(source.Host)
		if err != nil {
			return nil, err
		}
		root, err := os.OpenRoot(parent)
		if err != nil {
			return nil, err
		}
		absolute := parent + string(filepath.Separator) + name
		st, err := root.Stat(name)
		if err != nil {
			root.Close()
			return nil, copyInputError("copy_sensitive", "the copied source cannot be read inside its chosen folder")
		}
		guest := source.Guest
		if guest == "" {
			guest = defaultCopyGuest(defaultSource, home)
		}
		if !api.InputGuestPath(guest) {
			root.Close()
			return nil, copyInputError("bad_copy", "a copied file targets a reserved or invalid guest path")
		}
		if old, exists := targets[guest]; exists {
			root.Close()
			if old == absolute {
				continue
			}
			return nil, &UserError{Code: "copy_collision", Msg: "different local copies have the same guest target", Hint: "Choose a different explicit guest target for each source.", Kind: KindUsage}
		}
		targets[guest] = absolute
		if st.IsDir() {
			dir, err := files.OpenContainedDirectory(root, name)
			if err == nil {
				err = checkCopyPath(root, name, absolute, dir, folders)
			}
			if dir != nil {
				dir.Close()
			}
			if err != nil {
				root.Close()
				return nil, err
			}
			chosen, err := root.OpenRoot(name)
			root.Close()
			if err != nil {
				return nil, err
			}
			actual, err := chosen.Stat(".")
			if err == nil && !os.SameFile(st, actual) {
				err = copyInputError("copy_changed", "the copied directory changed before traversal")
			}
			if err == nil {
				err = plan.walk(ctx, chosen, ".", absolute, guest, limit, folders, nil)
			}
			chosen.Close()
			if err != nil {
				return nil, err
			}
		} else {
			err = plan.add(ctx, root, name, absolute, guest, limit, folders)
			root.Close()
			if err != nil {
				return nil, err
			}
		}
	}
	slices.SortFunc(plan.files, func(a, b plannedCopy) int { return strings.Compare(a.file.Path, b.file.Path) })
	all := make(map[string]bool, len(plan.files))
	for _, file := range plan.files {
		if all[file.file.Path] {
			return nil, &UserError{Code: "copy_collision", Msg: "copied files overlap in the guest", Hint: "Choose separate guest targets.", Kind: KindUsage}
		}
		all[file.file.Path] = true
	}
	for name := range all {
		for parent := path.Dir(name); parent != "/"; parent = path.Dir(parent) {
			if all[parent] {
				return nil, &UserError{Code: "copy_collision", Msg: "a copied file is another copy's parent", Hint: "Choose separate guest targets.", Kind: KindUsage}
			}
		}
	}
	return plan, nil
}

func checkCopySpelling(typed string, folders []privateFolder) error {
	if privateName(filepath.Base(typed)) != "" || inPrivateFolder(typed, folders) != "" {
		return copyInputError("copy_sensitive", "the copied source has a sensitive name or private folder")
	}
	resolved, err := filepath.EvalSymlinks(typed)
	if err != nil {
		return err
	}
	if privateName(filepath.Base(resolved)) != "" || inPrivateFolder(resolved, folders) != "" {
		return copyInputError("copy_sensitive", "the copied source aliases a sensitive name or private folder")
	}
	st, err := os.Stat(resolved)
	if err != nil {
		return err
	}
	for _, folder := range folders {
		if os.SameFile(st, folder.info) {
			return copyInputError("copy_sensitive", "the copied source aliases a private folder")
		}
	}
	return nil
}

func (p *copyPlan) walk(ctx context.Context, root *os.Root, name, typed, guest string, limit int64, folders []privateFolder, ancestors []os.FileInfo) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !api.InputGuestPath(guest) {
		return copyInputError("bad_copy", "a copied directory target is too long or reserved")
	}
	dir, err := files.OpenContainedDirectory(root, name)
	if err != nil {
		return err
	}
	defer dir.Close()
	if err = checkCopyPath(root, name, typed, dir, folders); err != nil {
		return err
	}
	st, err := dir.Stat()
	if err != nil {
		return err
	}
	for _, ancestor := range ancestors {
		if os.SameFile(st, ancestor) {
			return copyInputError("copy_cycle", "a copied directory contains a link cycle")
		}
	}
	ancestors = append(slices.Clone(ancestors), st)
	empty := true
	for {
		entries, readErr := dir.ReadDir(128)
		empty = empty && len(entries) == 0
		for _, entry := range entries {
			child := path.Join(name, entry.Name())
			if err := checkCopySpelling(filepath.Join(typed, entry.Name()), folders); err != nil {
				return err
			}
			info, err := root.Stat(child)
			if err != nil {
				return copyInputError("copy_sensitive", "a copied entry escapes its chosen folder or cannot be checked")
			}
			if info.IsDir() {
				err = p.walk(ctx, root, child, filepath.Join(typed, entry.Name()), path.Join(guest, entry.Name()), limit, folders, ancestors)
			} else {
				err = p.add(ctx, root, child, filepath.Join(typed, entry.Name()), path.Join(guest, entry.Name()), limit, folders)
			}
			if err != nil {
				return err
			}
		}
		if readErr == io.EOF {
			if empty {
				return p.addDirectory(root, name, guest, st, folders)
			}
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

func (p *copyPlan) addDirectory(root *os.Root, name, guest string, info os.FileInfo, folders []privateFolder) error {
	if len(p.files) >= api.CopyFileMax {
		return copyInputError("copy_too_many", "copied inputs exceed 10000 transferred leaves")
	}
	rootInfo, err := root.Stat(".")
	if err != nil {
		return err
	}
	p.files = append(p.files, plannedCopy{root: copyRoot{name: root.Name(), info: rootInfo}, name: name, info: info, folders: folders,
		file: api.InputFile{Path: guest, Mode: uint32(info.Mode().Perm()), SHA256: api.EmptyInputHash, Directory: true}})
	return nil
}

func (p *copyPlan) add(ctx context.Context, root *os.Root, name, typed, guest string, limit int64, folders []privateFolder) error {
	if len(p.files) >= api.CopyFileMax {
		return copyInputError("copy_too_many", "copied inputs exceed 10000 files")
	}
	f, err := files.OpenContainedRegular(root, name)
	if err != nil {
		return copyInputError("copy_sensitive", "a copied entry is not a readable contained regular file")
	}
	defer f.Close()
	if err = checkCopyPath(root, name, typed, f, folders); err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if st.Size() < 0 || st.Size() > limit-p.bytes {
		return copyInputError("copy_too_big", "copied inputs exceed copy_max")
	}
	sum := sha256.New()
	n, err := io.Copy(sum, &inputContextReader{ctx: ctx, reader: io.LimitReader(f, st.Size()+1)})
	if err != nil {
		return err
	}
	if n != st.Size() {
		return copyInputError("copy_changed", "a copied file changed while planning")
	}
	rootInfo, err := root.Stat(".")
	if err != nil {
		return err
	}
	p.files = append(p.files, plannedCopy{root: copyRoot{name: root.Name(), info: rootInfo}, name: name, info: st, folders: folders,
		file: api.InputFile{Path: guest, Size: n, Mode: uint32(st.Mode().Perm() & 0777), SHA256: hex.EncodeToString(sum.Sum(nil))}})
	p.bytes += n
	return nil
}

type inputContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *inputContextReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(data)
}

func (f plannedCopy) write(ctx context.Context, target io.Writer) error {
	root, err := f.root.open()
	if err != nil {
		return err
	}
	defer root.Close()
	if f.file.Directory {
		dir, err := files.OpenContainedDirectory(root, f.name)
		if err != nil {
			return err
		}
		defer dir.Close()
		st, err := dir.Stat()
		if err != nil || !os.SameFile(st, f.info) || !st.ModTime().Equal(f.info.ModTime()) {
			return copyInputError("copy_changed", "the copied empty directory changed")
		}
		if err = checkCopyPath(root, f.name, filepath.Join(root.Name(), filepath.FromSlash(f.name)), dir, append(slices.Clone(f.folders), privateFolderInfos()...)); err != nil {
			return err
		}
		entries, err := dir.ReadDir(1)
		if len(entries) != 0 || err != io.EOF {
			return copyInputError("copy_changed", "the copied directory is no longer empty")
		}
		return ctx.Err()
	}
	var file *os.File
	if f.include && f.file.Include {
		file, err = files.OpenIncluded(root, f.name)
	} else if !f.include && !f.file.Include {
		file, err = files.OpenContainedRegular(root, f.name)
	} else {
		return copyInputError("include_scope", "the reviewed source role changed")
	}
	if err != nil {
		return err
	}
	defer file.Close()
	st, err := file.Stat()
	if err != nil || !os.SameFile(st, f.info) || st.Size() != f.file.Size {
		return copyInputError("copy_changed", "a copied source changed before publication")
	}
	if !f.include {
		if err = checkCopyPath(root, f.name, filepath.Join(root.Name(), filepath.FromSlash(f.name)), file, append(slices.Clone(f.folders), privateFolderInfos()...)); err != nil {
			return err
		}
	}
	sum := sha256.New()
	n, err := io.Copy(io.MultiWriter(target, sum), &inputContextReader{ctx: ctx, reader: io.LimitReader(file, f.file.Size)})
	if err != nil {
		return fmt.Errorf("stream copied file: %w", err)
	}
	var extra [1]byte
	more, endErr := file.Read(extra[:])
	if n != f.file.Size || more != 0 || endErr != io.EOF || hex.EncodeToString(sum.Sum(nil)) != f.file.SHA256 {
		return copyInputError("copy_changed", "copied bytes changed after the declaration was sealed")
	}
	return nil
}
