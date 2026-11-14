package supply

import (
	"context"
	"fmt"
	"io"
	"os"
)

// CheckOpen checks an already open regular file against the same manifest
// rules as CheckFile. The caller owns the descriptor and keeps its bytes
// unchanged during the check. It leaves the offset unspecified.
func CheckOpen(it Item, f *os.File) error {
	return CheckOpenContext(context.Background(), it, f)
}

// CheckOpenContext applies the canonical descriptor checks while observing its
// parent before and after every read/seek. It owns no descriptor lifetime.
func CheckOpenContext(ctx context.Context, it Item, f *os.File) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat the open runtime file: %w", err)
	}
	if !st.Mode().IsRegular() || !singleLink(f, st) {
		return fmt.Errorf("the open runtime file is not regular")
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("seek the open runtime file: %w", err)
	}
	if err := checkReader(it, it.Name, st.Size(), contextualReadSeeker{ctx: ctx, file: f}); err != nil {
		return err
	}
	if err := checkToolsTree(it, contextualReadSeeker{ctx: ctx, file: f}); err != nil {
		return err
	}
	return ctx.Err()
}

// contextualReadSeeker does not expose WriterTo or ReaderFrom fast paths.
// The canonical streaming checks therefore observe every underlying read.
type contextualReadSeeker struct {
	ctx  context.Context
	file io.ReadSeeker
}

func (reader contextualReadSeeker) Read(data []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := reader.file.Read(data)
	if cancelled := reader.ctx.Err(); cancelled != nil {
		return n, cancelled
	}
	return n, err
}
func (reader contextualReadSeeker) Seek(offset int64, whence int) (int64, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	position, err := reader.file.Seek(offset, whence)
	if cancelled := reader.ctx.Err(); cancelled != nil {
		return position, cancelled
	}
	return position, err
}
