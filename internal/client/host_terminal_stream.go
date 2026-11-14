package client

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

var afterHostTerminalSync = func() {}

// hostTerminalStream projects deterministic framing around the unchanged
// owner receipt. The receipt length is checked before this view is created.
type hostTerminalStream struct {
	ctx     context.Context
	frame   hostTerminalFrame
	receipt io.ReaderAt
}

func (r hostTerminalStream) ReadAt(data []byte, offset int64) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	if offset < 0 {
		return 0, os.ErrInvalid
	}
	if len(data) == 0 {
		return 0, nil
	}
	read := 0
	for read < len(data) && offset < r.frame.total {
		if err := r.ctx.Err(); err != nil {
			return read, err
		}
		var n int
		switch {
		case offset < r.frame.receiptStart:
			n = copy(data[read:], r.frame.prefix[offset:])
		case offset < r.frame.receiptStart+r.frame.header.ReceiptSize:
			remaining := r.frame.receiptStart + r.frame.header.ReceiptSize - offset
			chunk := data[read:]
			if int64(len(chunk)) > remaining {
				chunk = chunk[:remaining]
			}
			var err error
			n, err = r.receipt.ReadAt(chunk, offset-r.frame.receiptStart)
			if err != nil && !(errors.Is(err, io.EOF) && n == len(chunk)) {
				return read + n, err
			}
			if n != len(chunk) {
				return read + n, io.ErrUnexpectedEOF
			}
		default:
			n = copy(data[read:], hostTerminalFooter[offset-r.frame.receiptStart-r.frame.header.ReceiptSize:])
		}
		read += n
		offset += int64(n)
	}
	if err := r.ctx.Err(); err != nil {
		return read, err
	}
	if read != len(data) {
		return read, io.EOF
	}
	return read, nil
}

func compareHostTerminalPrefix(ctx context.Context, existing, expected io.ReaderAt, size int64) error {
	var actual, wanted [32 << 10]byte
	for offset := int64(0); offset < size; {
		count := int64(len(actual))
		if count > size-offset {
			count = size - offset
		}
		if err := terminalReadAt(ctx, existing, actual[:count], offset); err != nil {
			return err
		}
		if err := terminalReadAt(ctx, expected, wanted[:count], offset); err != nil {
			return err
		}
		for i := int64(0); i < count; i++ {
			if actual[i] != wanted[i] {
				return hostStateRemovalError("the existing OP is not the exact reconstructible checkpoint prefix")
			}
		}
		offset += count
	}
	return ctx.Err()
}

func writeHostTerminalCheckpoint(ctx context.Context, held *heldLock, frame hostTerminalFrame, receipt stateFileView) error {
	if !receipt.Exists || receipt.ReaderAt == nil || receipt.Size != frame.header.ReceiptSize {
		return hostStateRemovalError("the checkpoint lost its exact existing source receipt")
	}
	if err := held.checkStateOperation(ctx); err != nil {
		return err
	}
	stream := hostTerminalStream{ctx: ctx, frame: frame, receipt: receipt.ReaderAt}
	info, err := held.native.file.Stat()
	if err != nil {
		return err
	}
	if info.Size() < 0 || info.Size() > frame.total {
		return hostStateRemovalError("the existing OP cannot be a prefix of this checkpoint")
	}
	if err := compareHostTerminalPrefix(ctx, held.native.file, stream, info.Size()); err != nil {
		return err
	}
	// Error unwind must retain the diagnostic before even the first byte.
	// This mode alone grants no owner proof or destructive authority.
	held.terminal.Store(true)
	var buffer [32 << 10]byte
	for offset := info.Size(); offset < frame.total; {
		if err := stateContextAuthority(ctx); err != nil {
			return err
		}
		if err := held.checkStateOperation(ctx); err != nil {
			return err
		}
		count := int64(len(buffer))
		if count > frame.total-offset {
			count = frame.total - offset
		}
		if err := terminalReadAt(ctx, stream, buffer[:count], offset); err != nil {
			return err
		}
		n, err := held.native.file.WriteAt(buffer[:count], offset)
		if err != nil {
			return err
		}
		if int64(n) != count {
			return io.ErrShortWrite
		}
		offset += count
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := held.native.file.Sync(); err != nil {
		return err
	}
	afterHostTerminalSync()
	if _, err := readHostTerminalFrame(ctx, held.native.file, frame.total); err != nil {
		return err
	}
	if err := compareHostTerminalPrefix(ctx, held.native.file, stream, frame.total); err != nil {
		return err
	}
	if err := stateContextAuthority(ctx); err != nil {
		return err
	}
	return held.checkStateOperation(ctx)
}

// A complete footer alone cannot prove a previous Sync returned. A retry
// syncs its SAME existing inode and rechecks it before deletion authority.
func syncExistingHostTerminalCheckpoint(ctx context.Context, held *heldLock, frame hostTerminalFrame) error {
	if err := stateContextAuthority(ctx); err != nil {
		return err
	}
	reference, err := cachefs.CaptureFile(held.native.root, held.native.name, held.native.file, 0600)
	if err != nil {
		return err
	}
	if err := held.native.file.Sync(); err != nil {
		return err
	}
	afterHostTerminalSync()
	fresh, err := readHostTerminalFrame(ctx, held.native.file, reference.Size)
	if err != nil {
		return err
	}
	if fresh.total != frame.total || !bytes.Equal(fresh.prefix, frame.prefix) {
		return hostStateRemovalError("the complete terminal frame changed during its durability check")
	}
	if err := reference.CheckFile(held.native.root, held.native.name, held.native.file, 0600); err != nil {
		return err
	}
	return stateContextAuthority(ctx)
}
