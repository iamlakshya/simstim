package supply

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Microsoft/hcsshim/ext4/dmverity"
)

const (
	// VerityBlockBytes is the 4 KiB block size used by dm-verity trees (spec 20.1).
	VerityBlockBytes = 4096
	// ZeroSaltHex is the fixed 32-zero-byte dm-verity salt for deterministic
	// tools images (spec 20.1 and 20.6).
	ZeroSaltHex = "0000000000000000000000000000000000000000000000000000000000000000"
)

// VerityInfo describes a tools image after padding and appending its raw
// SHA-256 dm-verity Merkle tree (no superblock, 32-zero-byte salt).
type VerityInfo struct {
	SHA256     string `json:"sha256"`
	Size       int64  `json:"size"`
	RootHash   string `json:"root_hash"`
	Salt       string `json:"salt"`
	DataBlocks uint64 `json:"data_blocks"`
}

// AppendRawVerity pads path with zeros to a 4096-byte multiple, appends a raw
// SHA-256 dm-verity Merkle tree (no superblock, 32-zero-byte salt) using
// hcsshim's ext4/dmverity package, and returns the resulting file metadata.
func AppendRawVerity(path string) (VerityInfo, error) {
	clean := filepath.Clean(path)
	f, err := os.OpenFile(clean, os.O_RDWR, 0)
	if err != nil {
		return VerityInfo{}, fmt.Errorf("verity: open %s: %w. Try: pass an existing writable image file", path, err)
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return VerityInfo{}, fmt.Errorf("verity: stat %s: %w", path, err)
	}
	dataSize := fi.Size()
	if dataSize <= 0 {
		return VerityInfo{}, fmt.Errorf("verity: %s is empty. Try: build the EROFS tools image before appending verity", path)
	}
	if rem := dataSize % VerityBlockBytes; rem != 0 {
		pad := make([]byte, VerityBlockBytes-rem)
		if _, err := f.Seek(0, io.SeekEnd); err != nil {
			return VerityInfo{}, fmt.Errorf("verity: seek %s: %w", path, err)
		}
		if _, err := f.Write(pad); err != nil {
			return VerityInfo{}, fmt.Errorf("verity: pad %s: %w", path, err)
		}
		dataSize += int64(len(pad))
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return VerityInfo{}, fmt.Errorf("verity: rewind %s: %w", path, err)
	}

	tree, err := dmverity.MerkleTree(io.LimitReader(f, dataSize))
	if err != nil {
		return VerityInfo{}, fmt.Errorf("verity: compute Merkle tree for %s: %w", path, err)
	}
	if len(tree) < VerityBlockBytes {
		return VerityInfo{}, fmt.Errorf("verity: Merkle tree for %s is unexpectedly short (%d bytes)", path, len(tree))
	}
	root := dmverity.RootHash(tree[:VerityBlockBytes])
	if _, err := f.Seek(dataSize, io.SeekStart); err != nil {
		return VerityInfo{}, fmt.Errorf("verity: seek end of data in %s: %w", path, err)
	}
	if _, err := f.Write(tree); err != nil {
		return VerityInfo{}, fmt.Errorf("verity: write Merkle tree to %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		return VerityInfo{}, fmt.Errorf("verity: sync %s: %w", path, err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return VerityInfo{}, fmt.Errorf("verity: rewind %s for SHA-256: %w", path, err)
	}
	h := sha256.New()
	totalSize, err := io.Copy(h, f)
	if err != nil {
		return VerityInfo{}, fmt.Errorf("verity: hash %s: %w", path, err)
	}
	dataBlocks := uint64(dataSize / VerityBlockBytes)
	return VerityInfo{
		SHA256:     hex.EncodeToString(h.Sum(nil)),
		Size:       totalSize,
		RootHash:   hex.EncodeToString(root),
		Salt:       ZeroSaltHex,
		DataBlocks: dataBlocks,
	}, nil
}

// VerifyRawVerity recomputes the raw SHA-256 dm-verity Merkle tree over the
// first dataBlocks*4096 bytes of path, checks that it matches the appended tree
// bytes in path and equals wantRootHash, and returns the file's SHA-256 & size.
func VerifyRawVerity(path string, dataBlocks uint64, wantRootHash string) (VerityInfo, error) {
	if dataBlocks == 0 {
		return VerityInfo{}, errors.New("verity: data_blocks must be positive. Try: pass the data_blocks recorded by guest/verity")
	}
	wantRoot := strings.ToLower(strings.TrimSpace(wantRootHash))
	if !isSHA256Hex(wantRoot) {
		return VerityInfo{}, fmt.Errorf("verity: invalid root hash %q. Try: pass a 64-character lowercase SHA-256 hex digest", wantRootHash)
	}
	clean := filepath.Clean(path)
	f, err := os.Open(clean)
	if err != nil {
		return VerityInfo{}, fmt.Errorf("verity: open %s: %w. Try: verify the tools image path exists", path, err)
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return VerityInfo{}, fmt.Errorf("verity: stat %s: %w", path, err)
	}
	if dataBlocks > uint64((1<<62)/VerityBlockBytes) {
		return VerityInfo{}, errors.New("verity: data_blocks overflows maximum file size. Try: check release-manifest.json")
	}
	dataSize := int64(dataBlocks * VerityBlockBytes)
	if fi.Size() <= dataSize {
		return VerityInfo{}, fmt.Errorf("verity: %s (%d bytes) has no appended Merkle tree after %d data bytes. Try: rebuild the tools image with guest/verity", path, fi.Size(), dataSize)
	}

	wantTree, err := dmverity.MerkleTree(io.LimitReader(f, dataSize))
	if err != nil {
		return VerityInfo{}, fmt.Errorf("verity: compute Merkle tree for %s: %w", path, err)
	}
	if len(wantTree) < VerityBlockBytes {
		return VerityInfo{}, fmt.Errorf("verity: Merkle tree for %s is unexpectedly short", path)
	}
	gotTree := make([]byte, len(wantTree))
	if fi.Size() != dataSize+int64(len(wantTree)) {
		return VerityInfo{}, fmt.Errorf("verity: %s size is %d bytes, want %d bytes (%d data + %d tree). Try: rebuild the tools image", path, fi.Size(), dataSize+int64(len(wantTree)), dataSize, len(wantTree))
	}
	if _, err := io.ReadFull(f, gotTree); err != nil {
		return VerityInfo{}, fmt.Errorf("verity: read appended Merkle tree in %s: %w", path, err)
	}
	if !equalBytes(gotTree, wantTree) {
		return VerityInfo{}, fmt.Errorf("verity: Merkle tree in %s does not match its data blocks. Try: rebuild or re-download the tools image", path)
	}
	gotRoot := hex.EncodeToString(dmverity.RootHash(wantTree[:VerityBlockBytes]))
	if gotRoot != wantRoot {
		return VerityInfo{}, fmt.Errorf("verity: %s root hash is %s, want %s. Try: rebuild or re-download the tools image", path, gotRoot, wantRoot)
	}

	fileSHA, fileSize, err := FileDigest(clean)
	if err != nil {
		return VerityInfo{}, err
	}
	return VerityInfo{
		SHA256:     fileSHA,
		Size:       fileSize,
		RootHash:   gotRoot,
		Salt:       ZeroSaltHex,
		DataBlocks: dataBlocks,
	}, nil
}

// FileDigest returns the lowercase SHA-256 hex digest and exact byte size of path.
func FileDigest(path string) (string, int64, error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return "", 0, fmt.Errorf("supply: open %s: %w", path, err)
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, fmt.Errorf("supply: hash %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
