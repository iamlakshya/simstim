package filecopy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path"
	"slices"
	"strings"
	"sync"
	"syscall"

	"github.com/iamlakshya/simstim/internal/files"
	"golang.org/x/text/unicode/norm"
)

// Target is a local filesystem plan, not a peer declaration or permission.
type Target struct {
	Path      string
	Size      int64
	Directory bool
}
type stagedFile struct {
	target                         Target
	name, hash, parentID, identity string
	published                      bool
}

// Batch holds actual rooted laptop destinations and owned staging files. Its
// caller retains the canonical local-source guard and review operation; this
// low-level primitive never supplies consent or selects remote values.
type Batch struct {
	mu                         sync.Mutex
	root                       *os.Root
	targets                    []Target
	files                      map[string]*stagedFile
	created                    []string
	replace, closed, committed bool
}

func collisionKey(name string) string { return strings.ToLower(norm.NFC.String(name)) }

// NewBatch checks EVERY destination before it creates any file or folder.
func NewBatch(root *os.Root, targets []Target, replace bool) (*Batch, error) {
	if root == nil || len(targets) > 10001 {
		return nil, failure("cp_path", "the local copy plan is unavailable or too large")
	}
	seen := map[string]Target{}
	for _, target := range targets {
		if err := Relative(target.Path); err != nil {
			return nil, err
		}
		if target.Size < 0 || target.Directory && target.Size != 0 {
			return nil, failure("cp_path", "the copy target has an invalid size")
		}
		key := collisionKey(target.Path)
		if _, exists := seen[key]; exists {
			return nil, failure("cp_path", "two copy destinations differ only in case or filename normalisation")
		}
		seen[key] = target
	}
	for _, target := range targets {
		for parent := path.Dir(target.Path); parent != "."; parent = path.Dir(parent) {
			if declared, ok := seen[collisionKey(parent)]; ok && !declared.Directory {
				return nil, failure("cp_path", "a copy file is also used as a destination folder")
			}
		}
		if err := preflightTarget(root, target, replace); err != nil {
			return nil, err
		}
	}
	held, err := root.OpenRoot(".")
	if err != nil {
		return nil, err
	}
	return &Batch{root: held, targets: slices.Clone(targets), files: map[string]*stagedFile{}, replace: replace}, nil
}

func preflightTarget(root *os.Root, target Target, replace bool) (err error) {
	current, err := root.OpenRoot(".")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, current.Close()) }()
	parts := strings.Split(target.Path, "/")
	for index, part := range parts {
		info, err := current.Lstat(part)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if index == len(parts)-1 {
			if !replace {
				return failure("cp_exists", "a copy destination already exists")
			}
			if target.Directory && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) || !target.Directory && info.IsDir() {
				return failure("cp_exists", "the copy destination has another filesystem type")
			}
			return nil
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return failure("cp_path", "a laptop copy destination traverses a link or non-directory")
		}
		next, err := OpenDirectoryRoot(current, part, false)
		if err != nil {
			return err
		}
		if err := current.Close(); err != nil {
			next.Close()
			return err
		}
		current = next
	}
	return nil
}

func (b *Batch) ensureDirectories(name string) error {
	var pending []string
	for current := name; current != "."; current = path.Dir(current) {
		pending = append(pending, current)
	}
	slices.Reverse(pending)
	for _, name := range pending {
		parent, err := OpenDirectoryRoot(b.root, path.Dir(name), false)
		if err != nil {
			return err
		}
		leaf := path.Base(name)
		info, statErr := parent.Lstat(leaf)
		if errors.Is(statErr, os.ErrNotExist) {
			err = parent.Mkdir(leaf, 0755)
			if err == nil {
				b.created = append(b.created, name)
			} else if errors.Is(err, os.ErrExist) {
				err = nil
			}
		} else if statErr != nil {
			err = statErr
		} else if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			err = failure("cp_path", "a destination folder is a link or changed type")
		}
		err = errors.Join(err, parent.Close())
		if err != nil {
			return err
		}
		opened, err := OpenDirectoryRoot(b.root, name, false)
		if err != nil {
			return err
		}
		if err := opened.Close(); err != nil {
			return err
		}
	}
	return nil
}

// Stage writes only an exact selected file under an owned temporary name.
// Nothing is published at its final name before the caller's complete review.
func (b *Batch) Stage(ctx context.Context, target Target, input io.Reader) (err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || target.Directory || input == nil {
		return failure("cp_path", "the file staging operation is closed or invalid")
	}
	if !slices.Contains(b.targets, target) || b.files[target.Path] != nil {
		return failure("cp_path", "the file differs from the original destination plan")
	}
	if err := b.ensureDirectories(path.Dir(target.Path)); err != nil {
		return err
	}
	parent, err := OpenDirectoryRoot(b.root, path.Dir(target.Path), false)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, parent.Close()) }()
	parentID, err := directoryIdentity(parent)
	if err != nil {
		return err
	}
	file, name, err := temporary(parent)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	identity, err := files.Identity(file)
	if err != nil {
		return errors.Join(err, parent.Remove(name))
	}
	stage := &stagedFile{target: target, name: name, parentID: parentID, identity: identity}
	b.files[target.Path] = stage
	hash := sha256.New()
	n, err := io.CopyN(io.MultiWriter(file, hash), contextReader{ctx, input}, target.Size)
	if err != nil || n != target.Size {
		return errors.Join(failure("cp_too_big", "the copied file is shorter than its listed size"), err)
	}
	var extra [1]byte
	nExtra, endErr := (contextReader{ctx, input}).Read(extra[:])
	if nExtra != 0 || !errors.Is(endErr, io.EOF) {
		return errors.Join(failure("cp_too_big", "the copied file sends bytes beyond its listed size"), endErr)
	}
	if err := file.Sync(); err != nil {
		return err
	}
	stage.hash = hex.EncodeToString(hash.Sum(nil))
	return ctx.Err()
}

func directoryIdentity(root *os.Root) (identity string, err error) {
	file, err := root.Open(".")
	if err != nil {
		return "", err
	}
	identity, err = files.Identity(file)
	return identity, errors.Join(err, file.Close())
}

// Reopen one exact owned stage at a time, keeping descriptor use independent
// of the permitted10000 entries. Every parent and file is checked by native ID.
func (b *Batch) openStaged(stage *stagedFile, writable bool) (_ *os.Root, _ *os.File, err error) {
	parent, err := OpenDirectoryRoot(b.root, path.Dir(stage.target.Path), false)
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, parent.Close())
		}
	}()
	identity, err := directoryIdentity(parent)
	if err != nil {
		return nil, nil, err
	}
	if identity != stage.parentID {
		return nil, nil, failure("cp_changed", "the staged parent changed before use")
	}
	var file *os.File
	if writable {
		file, err = files.OpenRegularWrite(parent, stage.name)
	} else {
		file, err = files.OpenRegular(parent, stage.name)
	}
	if err != nil {
		return nil, nil, err
	}
	identity, err = files.Identity(file)
	if err != nil || identity != stage.identity {
		return nil, nil, errors.Join(failure("cp_changed", "the staged name changed before use"), err, file.Close())
	}
	return parent, file, nil
}

// ReadStaged supplies bounded actual bytes for the canonical content scanner.
func (b *Batch) ReadStaged(ctx context.Context, name string, limit int64) (_ []byte, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	stage := b.files[name]
	if b.closed || stage == nil || stage.hash == "" || stage.target.Size > limit {
		return nil, failure("cp_path", "the sealed review file is unavailable or exceeds its read bound")
	}
	parent, file, err := b.openStaged(stage, false)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, file.Close(), parent.Close()) }()
	data, err := io.ReadAll(io.LimitReader(contextReader{ctx, file}, stage.target.Size+1))
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(data)
	if int64(len(data)) != stage.target.Size || hex.EncodeToString(hash[:]) != stage.hash {
		return nil, failure("cp_changed", "the staged file changed before review")
	}
	return data, nil
}

// Commit follows the caller's complete original-source/content/consent gates.
// Link is atomic no-replace; explicit replacement renames over the leaf itself.
func (b *Batch) Commit(ctx context.Context) (err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return failure("cp_path", "the local copy transaction is closed")
	}
	for _, target := range b.targets {
		if !target.Directory {
			stage := b.files[target.Path]
			if stage == nil || stage.hash == "" {
				return failure("cp_path", "the copy transaction has an unfinished file")
			}
			if err := b.verifyStage(ctx, stage); err != nil {
				return err
			}
		}
	}
	for _, target := range b.targets {
		if target.Directory {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := b.ensureDirectories(target.Path); err != nil {
				return err
			}
		}
	}
	for _, target := range b.targets {
		if target.Directory {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		stage := b.files[target.Path]
		parent, file, openErr := b.openStaged(stage, true)
		if openErr != nil {
			return openErr
		}
		if err = file.Chmod(0644); err == nil {
			if b.replace {
				err = parent.Rename(stage.name, path.Base(target.Path))
			} else {
				err = parent.Link(stage.name, path.Base(target.Path))
			}
			if err == nil {
				stage.published = true
				if !b.replace {
					err = parent.Remove(stage.name)
				}
				if err == nil {
					stage.name = ""
				}
			}
		}
		err = errors.Join(err, file.Close(), parent.Close())
		if errors.Is(err, os.ErrExist) {
			return failure("cp_exists", "a destination appeared after copy preflight")
		}
		if err != nil {
			return err
		}
	}
	b.committed = true
	return nil
}

func (b *Batch) verifyStage(ctx context.Context, stage *stagedFile) (err error) {
	parent, file, err := b.openStaged(stage, false)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, file.Close(), parent.Close()) }()
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(contextReader{ctx, file}, stage.target.Size+1))
	if err != nil {
		return err
	}
	if n != stage.target.Size || hex.EncodeToString(hash.Sum(nil)) != stage.hash {
		return failure("cp_changed", "the reviewed staged bytes changed before publication")
	}
	return ctx.Err()
}

// Close removes only exact transient names created by this transaction. It
// preserves published files and never sweeps a directory or extracts an archive.
func (b *Batch) Close() (err error) {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	for _, stage := range b.files {
		if stage.name == "" {
			continue
		}
		parent, file, openErr := b.openStaged(stage, false)
		if openErr != nil {
			err = errors.Join(err, openErr)
			continue
		}
		removeErr := parent.Remove(stage.name)
		if !errors.Is(removeErr, os.ErrNotExist) {
			err = errors.Join(err, removeErr)
		}
		err = errors.Join(err, file.Close(), parent.Close())
	}
	if !b.committed {
		for index := len(b.created) - 1; index >= 0; index-- {
			name := b.created[index]
			parent, openErr := OpenDirectoryRoot(b.root, path.Dir(name), false)
			if openErr != nil {
				err = errors.Join(err, openErr)
				continue
			}
			removeErr := parent.Remove(path.Base(name))
			if removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) && !errors.Is(removeErr, syscall.ENOTEMPTY) && !errors.Is(removeErr, os.ErrExist) {
				err = errors.Join(err, removeErr)
			}
			err = errors.Join(err, parent.Close())
		}
	}
	return errors.Join(err, b.root.Close())
}
