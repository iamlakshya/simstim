package guest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path"

	"github.com/iamlakshya/simstim/internal/filecopy"
	"github.com/iamlakshya/simstim/internal/proto"
)

func packageSettingsParent(ctx context.Context, root *os.Root, relative string, create bool) (*os.Root, error) {
	parent := path.Dir(relative)
	if parent == "." {
		return root.OpenRoot(".")
	}
	witness, err := filecopy.OpenDirectory(root, parent)
	if errors.Is(err, os.ErrNotExist) && create {
		if err := filecopy.MakeDirectory(ctx, root, parent, bytes.NewReader(nil)); err != nil {
			return nil, err
		}
		witness, err = filecopy.OpenDirectory(root, parent)
	}
	if err != nil {
		return nil, err
	}
	info, err := witness.Stat()
	if err != nil {
		return nil, errors.Join(err, witness.Close())
	}
	opened, err := root.OpenRoot(parent)
	if err != nil {
		return nil, errors.Join(err, witness.Close())
	}
	actual, err := opened.Stat(".")
	if err != nil || !os.SameFile(info, actual) {
		return nil, errors.Join(packageManagerFailure("a package settings parent changed while opening"), err, opened.Close(), witness.Close())
	}
	if err := witness.Close(); err != nil {
		return nil, errors.Join(err, opened.Close())
	}
	return opened, nil
}

func preflightPackageSettingsFile(ctx context.Context, root *os.Root, relative string) (returned error) {
	parent, err := packageSettingsParent(ctx, root, relative, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, parent.Close()) }()
	_, err = parent.Lstat(path.Base(relative))
	if !errors.Is(err, os.ErrNotExist) {
		return packageManagerFailure("a package settings destination already exists or cannot be checked")
	}
	return nil
}

func writePackageManagerFiles(ctx context.Context, root *os.Root, plan *proto.PackageManagersV1) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, files, err := proto.RenderPackageManagerSettings(plan)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return nil
	}
	if root == nil {
		return packageManagerFailure("the fixed package settings home is unavailable")
	}
	for _, file := range files {
		if err := preflightPackageSettingsFile(ctx, root, file.Path); err != nil {
			return err
		}
	}
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := writePackageSettingsFile(ctx, root, file); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func writePackageSettingsFile(ctx context.Context, root *os.Root, setting proto.PackageHomeFile) (returned error) {
	parent, err := packageSettingsParent(ctx, root, setting.Path, true)
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, parent.Close()) }()
	if err := ctx.Err(); err != nil {
		return err
	}
	file, err := parent.OpenFile(path.Base(setting.Path), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, file.Close()) }()
	if err := file.Chmod(0600); err != nil {
		return err
	}
	if count, err := io.WriteString(file, setting.Content); err != nil || count != len(setting.Content) {
		return errors.Join(io.ErrShortWrite, err)
	}
	// There is no public temporary-name cleanup. A failed boot's actual
	// fresh home is joined/reset by its runtime owner, never by foreign unlink.
	return file.Sync()
}
