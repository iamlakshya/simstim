package supply

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
)

const fsVerityBlock = 4096

// FSVerityDigest computes the Linux fs-verity file digest, with SHA-256,
// 4096-byte blocks and no salt. It hashes the 256-byte descriptor, rather
// than returning either the file's SHA-256 or the Merkle root alone.
// Memory is bounded by one block per tree level; no tree is spooled.
func FSVerityDigest(ctx context.Context, data io.ReaderAt, size int64) (string, error) {
	if size < 0 || data == nil {
		return "", fmt.Errorf("fs-verity needs a reader and nonnegative file size")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var root [sha256.Size]byte
	if size > 0 {
		counts := []int64{(size-1)/fsVerityBlock + 1}
		for counts[len(counts)-1] > 1 {
			counts = append(counts, (counts[len(counts)-1]-1)/128+1)
		}
		var err error
		root, err = fsVerityNode(ctx, data, size, counts, len(counts)-1, 0)
		if err != nil {
			return "", err
		}
	}
	var descriptor [256]byte
	descriptor[0], descriptor[1], descriptor[2] = 1, 1, 12
	binary.LittleEndian.PutUint64(descriptor[8:16], uint64(size))
	copy(descriptor[16:80], root[:])
	digest := sha256.Sum256(descriptor[:])
	return hex.EncodeToString(digest[:]), nil
}

func fsVerityNode(ctx context.Context, data io.ReaderAt, size int64, counts []int64, level int, index int64) ([32]byte, error) {
	var block [fsVerityBlock]byte
	if err := ctx.Err(); err != nil {
		return [32]byte{}, err
	}
	if level == 0 {
		offset := index * fsVerityBlock
		length := min(int64(len(block)), size-offset)
		n, err := data.ReadAt(block[:length], offset)
		if int64(n) != length || (err != nil && err != io.EOF) {
			return [32]byte{}, fmt.Errorf("read fs-verity data block: %w", errorsOrUnexpectedEOF(err))
		}
	} else {
		first := index * 128
		for i := int64(0); i < 128 && first+i < counts[level-1]; i++ {
			digest, err := fsVerityNode(ctx, data, size, counts, level-1, first+i)
			if err != nil {
				return [32]byte{}, err
			}
			copy(block[i*32:], digest[:])
		}
	}
	if err := ctx.Err(); err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(block[:]), nil
}

func errorsOrUnexpectedEOF(err error) error {
	if err == nil || err == io.EOF {
		return io.ErrUnexpectedEOF
	}
	return err
}

func fsVerityKind(kind string) bool { return kind == KindVMM || kind == KindKernel }

func (it Item) validateFSVerity() error {
	if it.FSVerity != "" && (!fsVerityKind(it.Kind) || !IsHex64(it.FSVerity)) {
		return fmt.Errorf("fsverity needs a VMM or kernel and 64 lower-case hex digits")
	}
	return nil
}

func checkFSVerityContent(ctx context.Context, it Item, data io.ReaderAt) error {
	if err := it.validateFSVerity(); err != nil {
		return err
	}
	if it.FSVerity == "" {
		return checkKernelEFIItemV1(ctx, it, data) // Older manifests retain their sealed-copy path.
	}
	got, err := FSVerityDigest(ctx, data, it.Size)
	if err != nil {
		return err
	}
	if got != it.FSVerity {
		return &MismatchError{Name: it.Name, What: "fs-verity", Want: it.FSVerity, Got: got}
	}
	return checkKernelEFIItemV1(ctx, it, data)
}
