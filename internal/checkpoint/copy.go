package checkpoint

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

// CopyOwned copies an original private disk to a NEW leaf below a retained
// destination root, using FICLONE where possible and a sparse copy otherwise.
// check must retain the original source and freeze lease throughout the copy.
// A failure removes only this creator's unfinished file. Successful output is
// a descriptor owned by the caller and must be closed after native adoption.
func CopyOwned(ctx context.Context, destinationRoot *os.Root, leaf string, source *os.File, check func(context.Context) error) (_ *os.File, returned error) {
	if ctx == nil || destinationRoot == nil || source == nil || check == nil {
		return nil, failure("the fork copy has no retained source or destination owner", nil)
	}
	if err := errors.Join(ctx.Err(), check(ctx)); err != nil {
		return nil, err
	}
	before, err := cachefs.ValidateDescriptor(source, 0o600)
	if err != nil {
		return nil, failure("cannot identify the fork's original disk", err)
	}
	part, err := createPart(destinationRoot)
	if err != nil {
		return nil, failure("cannot create the fork disk", err)
	}
	complete := false
	defer func() {
		if !complete {
			returned = errors.Join(returned, part.remove())
		}
	}()
	if err := cloneFile(part.file, source); err != nil {
		if !cloneUnsupported(err) {
			return nil, failure("cannot clone the fork disk", err)
		}
		if err := part.file.Truncate(0); err != nil {
			return nil, failure("cannot reset the original incomplete fork disk", err)
		}
		if err := sparseCopy(ctx, part.file, source, before.Size()); err != nil {
			return nil, failure("cannot copy the fork disk", err)
		}
	}
	if err := errors.Join(ctx.Err(), check(ctx), unchanged(source, before), part.file.Sync()); err != nil {
		return nil, failure("cannot finish the fork disk copy", err)
	}
	if err := part.publish(leaf); err != nil {
		return nil, failure("cannot publish the fork disk", err)
	}
	if err := errors.Join(syncRoot(destinationRoot), check(ctx), ctx.Err()); err != nil {
		return nil, failure("cannot commit the fork disk copy", err)
	}
	complete = true
	return part.file, nil
}

func sparseCopy(ctx context.Context, target, source *os.File, size int64) error {
	buffer := make([]byte, 1<<20)
	zero := make([]byte, len(buffer))
	for offset := int64(0); offset < size; {
		if err := ctx.Err(); err != nil {
			return err
		}
		block := buffer[:min(int64(len(buffer)), size-offset)]
		n, err := source.ReadAt(block, offset)
		if n != len(block) || err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("read sparse disk at %d: %w", offset, errors.Join(err, io.ErrUnexpectedEOF))
		}
		if !bytes.Equal(block, zero[:len(block)]) {
			if n, err := target.WriteAt(block, offset); n != len(block) || err != nil {
				return fmt.Errorf("write sparse disk at %d: %w", offset, errors.Join(err, io.ErrShortWrite))
			}
		}
		offset += int64(len(block))
	}
	if err := target.Truncate(size); err != nil {
		return failure("cannot retain the sparse fork disk's logical size", err)
	}
	return nil
}
