package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type stateRootAnchor struct {
	path string
	info os.FileInfo
}

func openStateDirRoot(subdirs []string, name string, create bool) (*os.Root, error) {
	r, _, err := openStateDirRootContext(context.Background(), subdirs, name, create)
	return r, err
}

func openStateDirRootContext(ctx context.Context, subdirs []string, name string, create bool) (*os.Root, []stateRootAnchor, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	for _, sub := range subdirs {
		if !validStateComponent(sub) {
			return nil, nil, badGrantStateError(name)
		}
	}
	path := StateDir()
	st, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if !create {
			return nil, []stateRootAnchor{{path: path}}, os.ErrNotExist
		}
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if err := os.MkdirAll(path, 0700); err != nil {
			return nil, nil, fmt.Errorf("make the state folder: %w", err)
		}
		st, err = os.Lstat(path)
	}
	if err != nil {
		return nil, nil, err
	}
	if !statePrivateDirectory(st) || !stateBaseDirectoryOwned(st) {
		return nil, nil, badGrantStateError(name)
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	r, err := os.OpenRoot(path)
	if err != nil {
		return nil, nil, err
	}
	if err := stateDirMatchesContext(ctx, r, st); err != nil {
		_ = r.Close()
		return nil, nil, err
	}
	anchors := []stateRootAnchor{{path: path, info: st}}
	for _, sub := range subdirs {
		if err := ctx.Err(); err != nil {
			_ = r.Close()
			return nil, nil, err
		}
		// No caller can change the directory or lock domain through a component.
		if sub == "" || sub == "." || sub == ".." || filepath.Base(sub) != sub || filepath.IsAbs(sub) {
			_ = r.Close()
			return nil, nil, badGrantStateError(name)
		}
		child, st, err := openStateChildRootContext(ctx, r, sub, name, create)
		_ = r.Close()
		if err != nil {
			if errors.Is(err, os.ErrNotExist) && !create {
				return nil, append(anchors, stateRootAnchor{path: filepath.Join(path, sub)}), err
			}
			return nil, nil, err
		}
		r = child
		path = filepath.Join(path, sub)
		anchors = append(anchors, stateRootAnchor{path: path, info: st})
	}
	return r, anchors, nil
}

func statePrivateDirectory(st os.FileInfo) bool {
	return st != nil && st.IsDir() && st.Mode()&os.ModeSymlink == 0 &&
		stateDirectoryOwned(st)
}

func openStateChildRoot(r *os.Root, name, fileName string, create bool) (*os.Root, error) {
	child, _, err := openStateChildRootContext(context.Background(), r, name, fileName, create)
	return child, err
}

func openStateChildRootContext(ctx context.Context, r *os.Root, name, fileName string, create bool) (*os.Root, os.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if create {
		if err := r.Mkdir(name, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	st, err := r.Lstat(name)
	if err != nil {
		return nil, nil, err
	}
	if !statePrivateDirectory(st) {
		return nil, nil, badGrantStateError(fileName)
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	child, err := r.OpenRoot(name)
	if err != nil {
		return nil, nil, err
	}
	if err := stateDirMatchesContext(ctx, child, st); err != nil {
		_ = child.Close()
		return nil, nil, err
	}
	return child, st, nil
}

func stateDirMatches(r *os.Root, before os.FileInfo) bool {
	return stateDirMatchesContext(context.Background(), r, before) == nil
}

func stateDirMatchesContext(ctx context.Context, r *os.Root, before os.FileInfo) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f, err := r.Open(".")
	if err != nil {
		return err
	}
	defer f.Close()
	if err := ctx.Err(); err != nil {
		return err
	}
	after, err := f.Stat()
	if stopped := ctx.Err(); stopped != nil {
		return stopped
	}
	if err != nil {
		return err
	}
	if !after.IsDir() || !os.SameFile(before, after) || after.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return badGrantStateError("state folder")
	}
	return stateSafeRoot(ctx, r)
}

func stateRegularMatches(r *os.Root, name string, f *os.File) bool {
	return stateRegularMatchesContext(context.Background(), r, name, f) == nil
}

func stateRegularMatchesContext(ctx context.Context, r *os.Root, name string, f *os.File) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	named, err := r.Lstat(name)
	if stopped := ctx.Err(); stopped != nil {
		return stopped
	}
	if err != nil {
		return err
	}
	if !named.Mode().IsRegular() {
		return badGrantStateError(name)
	}
	opened, err := f.Stat()
	if stopped := ctx.Err(); stopped != nil {
		return stopped
	}
	if err != nil {
		return err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(named, opened) {
		return badGrantStateError(name)
	}
	if _, err := stateSafeDescriptor(ctx, f); err != nil {
		return err
	}
	return nil
}

func validStateComponent(name string) bool {
	return name != "" && name != "." && name != ".." && filepath.Base(name) == name && !filepath.IsAbs(name) && filepath.VolumeName(name) == "" && !strings.ContainsAny(name, "/\\\x00:")
}

// Cleanup identity is separate from the final requested write mode. Only an
// exclusively created, native-owned temp may use this finite finalizer path.
func stateRegularIdentityMatches(r *os.Root, name string, f *os.File) bool {
	named, err := r.Lstat(name)
	if err != nil || !named.Mode().IsRegular() {
		return false
	}
	opened, err := f.Stat()
	return err == nil && opened.Mode().IsRegular() && os.SameFile(named, opened)
}

func stateRootMissing(err error, anchors []stateRootAnchor) bool {
	return errors.Is(err, os.ErrNotExist) && len(anchors) > 0 && anchors[len(anchors)-1].info == nil
}
