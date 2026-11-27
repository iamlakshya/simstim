package checkpoint

import (
	"context"
	"errors"
	"os"
)

// ProbeRootsV1 tests only fresh files in two actual retained directories.
// It opens no current VM disk and grants no running or stopped source.
func ProbeRootsV1(ctx context.Context, sourceRoot, targetRoot *os.Root, check func(context.Context) error) (returned error) {
	if ctx == nil || sourceRoot == nil || targetRoot == nil || check == nil {
		return failure("the filesystem probe has no original owned directories", nil)
	}
	if err := errors.Join(ctx.Err(), check(ctx)); err != nil {
		return err
	}
	source, err := createPart(sourceRoot)
	if err != nil {
		return failure("cannot create the source filesystem clone probe", err)
	}
	defer func() { returned = errors.Join(returned, source.remove()) }()
	target, err := createPart(targetRoot)
	if err != nil {
		return failure("cannot create the target filesystem clone probe", err)
	}
	defer func() { returned = errors.Join(returned, target.remove()) }()
	if _, err := source.file.WriteString("checkpoint clone probe\n"); err != nil {
		return err
	}
	if err := source.file.Sync(); err != nil {
		return err
	}
	if err := cloneFile(target.file, source.file); err != nil {
		return cloneFailure("the session filesystem clone probe failed", err)
	}
	return errors.Join(ctx.Err(), check(ctx))
}
