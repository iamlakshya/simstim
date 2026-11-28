package image

import (
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/Microsoft/hcsshim/ext4/dmverity"
	"github.com/Microsoft/hcsshim/ext4/tar2ext4"
	v1 "github.com/google/go-containerregistry/pkg/v1"
)

const verityBlockSize = 4096

// ConvertOptions configures OCI-to-ext4 conversion.
type ConvertOptions struct {
	Limits           Limits
	ExpectedRootHash string
	Logger           *slog.Logger
}

// ConvertResult describes a converted read-only ext4 + dm-verity disk image.
type ConvertResult struct {
	RootHash   string
	DataBlocks uint64
	Size       int64
	Stats      FlattenStats
}

// ConvertImage flattens img, converts it to a read-only ext4 file with an
// appended dm-verity superblock and Merkle tree via hcsshim tar2ext4, verifies
// the root hash if opts.ExpectedRootHash is set, and atomically writes outPath
// with mode 0444.
func ConvertImage(img v1.Image, ref, outPath string, opts ConvertOptions) (res ConvertResult, err error) {
	dir := filepath.Dir(outPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return res, fmt.Errorf("create image directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".rootfs-*.tmp")
	if err != nil {
		return res, fmt.Errorf("create temporary image file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		_ = tmp.Close()
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	pr, pw := io.Pipe()
	type flatRes struct {
		stats FlattenStats
		err   error
	}
	done := make(chan flatRes, 1)
	go func() {
		stats, ferr := Flatten(img, ref, opts.Limits, pw)
		_ = pw.CloseWithError(ferr)
		done <- flatRes{stats: stats, err: ferr}
	}()

	convErr := tar2ext4.Convert(pr, tmp, tar2ext4.AppendDMVerity)
	_ = pr.Close()
	flat := <-done
	res.Stats = flat.stats

	if flat.err != nil {
		return res, flat.err
	}
	if convErr != nil {
		return res, badLayer(ref, fmt.Sprintf("convert to ext4: %v", convErr), convErr)
	}

	if res.Stats.DroppedDevices > 0 && opts.Logger != nil {
		opts.Logger.Info("dropped device nodes from image", "ref", ref, "count", res.Stats.DroppedDevices)
	}

	rootHash, dataBlocks, size, err := InspectExt4Verity(tmp)
	if err != nil {
		return res, fmt.Errorf("inspect ext4 image: %w", err)
	}
	if opts.ExpectedRootHash != "" && !strings.EqualFold(rootHash, opts.ExpectedRootHash) {
		return res, rootMismatch(ref, rootHash, opts.ExpectedRootHash)
	}

	if err := tmp.Sync(); err != nil {
		return res, fmt.Errorf("sync image file: %w", err)
	}
	//nolint:gosec // G302: read-only rootfs image, spec 20.2 sets mode 0444
	if err := tmp.Chmod(0o444); err != nil {
		return res, fmt.Errorf("set image mode: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return res, fmt.Errorf("close image file: %w", err)
	}
	if err := os.Rename(tmpPath, outPath); err != nil {
		return res, fmt.Errorf("install image file: %w", err)
	}
	cleanup = false

	res.RootHash = rootHash
	res.DataBlocks = dataBlocks
	res.Size = size
	return res, nil
}

// ConvertTar converts a pre-flattened tar stream to an ext4 + dm-verity file in
// w and returns its root hash and data block count.
func ConvertTar(r io.Reader, w io.ReadWriteSeeker) (rootHash string, dataBlocks uint64, err error) {
	if err := tar2ext4.Convert(r, w, tar2ext4.AppendDMVerity); err != nil {
		return "", 0, fmt.Errorf("convert tar to ext4: %w", err)
	}
	rh, db, _, err := InspectExt4Verity(w)
	if err != nil {
		return "", 0, err
	}
	return rh, db, nil
}

// InspectExt4Verity reads the ext4 superblock and the appended dm-verity
// superblock and first Merkle tree block, returning the lowercase hex root hash
// (computed via dmverity.RootHash), the number of 4 KiB ext4 data blocks, and
// the total file size in bytes.
func InspectExt4Verity(rs io.ReadSeeker) (rootHash string, dataBlocks uint64, totalSize int64, err error) {
	totalSize, err = rs.Seek(0, io.SeekEnd)
	if err != nil {
		return "", 0, 0, fmt.Errorf("measure image size: %w", err)
	}
	if _, err := rs.Seek(0, io.SeekStart); err != nil {
		return "", 0, 0, fmt.Errorf("seek image start: %w", err)
	}

	fsSize, blkSize, err := tar2ext4.Ext4FileSystemSize(rs)
	if err != nil {
		return "", 0, 0, fmt.Errorf("read ext4 superblock: %w", err)
	}
	if blkSize != verityBlockSize || fsSize <= 0 || fsSize%verityBlockSize != 0 {
		return "", 0, 0, fmt.Errorf("unexpected ext4 geometry: size=%d block_size=%d", fsSize, blkSize)
	}
	dataBlocks = uint64(fsSize / verityBlockSize)

	if _, err := rs.Seek(fsSize, io.SeekStart); err != nil {
		return "", 0, 0, fmt.Errorf("seek dm-verity superblock: %w", err)
	}
	vi, err := dmverity.ReadDMVerityInfoReader(rs)
	if err != nil {
		return "", 0, 0, fmt.Errorf("read dm-verity superblock: %w", err)
	}
	if vi.DataBlocks != dataBlocks {
		return "", 0, 0, fmt.Errorf("dm-verity data blocks %d differ from ext4 %d", vi.DataBlocks, dataBlocks)
	}

	// Also compute the root hash directly from the first block of the Merkle
	// tree via dmverity.RootHash (spec 20.2 step 5).
	if _, err := rs.Seek(fsSize+verityBlockSize, io.SeekStart); err != nil {
		return "", 0, 0, fmt.Errorf("seek dm-verity tree: %w", err)
	}
	firstTreeBlock := make([]byte, verityBlockSize)
	if _, err := io.ReadFull(rs, firstTreeBlock); err != nil {
		return "", 0, 0, fmt.Errorf("read dm-verity root block: %w", err)
	}
	computed := hex.EncodeToString(dmverity.RootHash(firstTreeBlock))
	if computed != vi.RootDigest {
		return "", 0, 0, fmt.Errorf("dm-verity root hash mismatch: computed %s, header %s", computed, vi.RootDigest)
	}
	return computed, dataBlocks, totalSize, nil
}
