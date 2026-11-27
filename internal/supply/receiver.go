package supply

import (
	"context"
	"fmt"
	"io"
	"os"
)

// TransferChunkSize is the largest runtime or helper transfer chunk. Expected
// total size still comes from the caller's canonically validated manifest.
const TransferChunkSize = 64 << 10

// Receiver writes a bounded ordered stream into a descriptor owned by its
// caller. The caller supplies synchronisation, ownership checks and cleanup.
// Receiver never truncates, closes, unlinks or selects a filesystem path.
type Receiver struct {
	item     Item
	file     *os.File
	received int64
	failed   error
}

// NewReceiver validates an item and an empty regular single-link descriptor.
// The caller must first validate the complete release manifest, including its
// release URL authority, and keep this descriptor's identity under its lock.
func NewReceiver(item Item, file *os.File) (*Receiver, error) {
	if err := item.validate(); err != nil {
		return nil, fmt.Errorf("validate transfer item: %w", err)
	}
	if item.Kind == KindImage || !IsHex64(item.SHA256) || item.Size <= 0 {
		return nil, fmt.Errorf("transfer item needs a positive size and SHA-256")
	}
	if file == nil {
		return nil, fmt.Errorf("transfer needs an open descriptor")
	}
	receiver := &Receiver{item: item, file: file}
	if err := receiver.checkSize(); err != nil {
		return nil, err
	}
	return receiver, nil
}

// Received returns the number of bytes accepted by this receiver. Like other
// methods, it is called serially under the caller's transfer lock.
func (r *Receiver) Received() int64 {
	if r == nil {
		return 0
	}
	return r.received
}

// WriteAt accepts one nonempty chunk at the exact next offset. It refuses an
// oversized chunk before any write and stops permanently after any failure.
// Descriptor-relative WriteAt leaves the descriptor's seek offset unchanged.
func (r *Receiver) WriteAt(data []byte, offset int64) error {
	if r == nil || r.file == nil {
		return fmt.Errorf("transfer receiver has no open descriptor")
	}
	if r.failed != nil {
		return r.failed
	}
	if offset != r.received || len(data) == 0 || len(data) > TransferChunkSize || int64(len(data)) > r.item.Size-r.received {
		return r.fail(fmt.Errorf("transfer chunk has the wrong offset or exceeds its expected size"))
	}
	if err := r.checkSize(); err != nil {
		return r.fail(err)
	}
	n, err := r.file.WriteAt(data, r.received)
	r.received += int64(n)
	if err != nil {
		return r.fail(fmt.Errorf("write transfer chunk: %w", err))
	}
	if n != len(data) {
		return r.fail(fmt.Errorf("write transfer chunk: %w", io.ErrShortWrite))
	}
	return nil
}

// Finish requires the full expected stream, then applies the one canonical
// descriptor verifier, including tools Merkle verification. It performs no
// publication, permissions change, sync or cleanup on the caller's behalf.
func (r *Receiver) Finish() error { return r.FinishContext(context.Background()) }

// FinishContext verifies the same stream with its caller's cancellation. It
// never closes or removes the caller's descriptor on any outcome.
func (r *Receiver) FinishContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r == nil || r.file == nil {
		return fmt.Errorf("transfer receiver has no open descriptor")
	}
	if r.failed != nil {
		return r.failed
	}
	if r.received != r.item.Size {
		return r.fail(fmt.Errorf("transfer received %d of %d expected bytes", r.received, r.item.Size))
	}
	if err := CheckOpenContext(ctx, r.item, r.file); err != nil {
		return r.fail(fmt.Errorf("verify transfer descriptor: %w", err))
	}
	return nil
}

func (r *Receiver) checkSize() error {
	info, err := r.file.Stat()
	if err != nil {
		return fmt.Errorf("inspect transfer descriptor: %w", err)
	}
	if !info.Mode().IsRegular() || !singleLink(r.file, info) {
		return fmt.Errorf("transfer descriptor is not a regular single-link file")
	}
	if info.Size() != r.received {
		return fmt.Errorf("transfer descriptor has %d bytes, expected %d", info.Size(), r.received)
	}
	return nil
}

func (r *Receiver) fail(err error) error { r.failed = err; return err }
