package filecopy

import (
	"context"
	"errors"
	"io"
	"os"
)

// MakeDirectory receives an explicit empty-directory declaration after the
// same fixed-account/root/command-cap admission as ReceiveFile. It never
// replaces an existing file/link or follows an existing directory link.
func MakeDirectory(ctx context.Context, root *os.Root, relative string, input io.Reader) (err error) {
	if root == nil || input == nil {
		return failure("cp_path", "the directory declaration is incomplete")
	}
	if err := Relative(relative); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var extra [1]byte
	if n, endErr := input.Read(extra[:]); n != 0 || !errors.Is(endErr, io.EOF) {
		return errors.Join(failure("cp_path", "an empty directory declaration carries file bytes"), endErr)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	opened, err := directory(root, relative, true)
	if err != nil {
		return err
	}
	return errors.Join(ctx.Err(), opened.Close())
}
