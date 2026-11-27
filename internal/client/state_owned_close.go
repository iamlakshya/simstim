package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

type stateOwnedCloseKey struct{}

// The scoped observer can add a failure AFTER real owned closure for source
// ordering tests. It cannot suppress a native failure or supply permission.
func stateOwnedCloseResult(ctx context.Context, name string, native error) error {
	if observe, _ := ctx.Value(stateOwnedCloseKey{}).(func(string) error); observe != nil {
		return errors.Join(native, observe(name))
	}
	return native
}

func closeStateOwnedFile(ctx context.Context, file *os.File) error {
	return stateOwnedCloseResult(ctx, filepath.Base(file.Name()), file.Close())
}

func closeStateOwnedRoot(ctx context.Context, root *os.Root) error {
	return stateOwnedCloseResult(ctx, "state-root", root.Close())
}
