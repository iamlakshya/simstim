package chv

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Microsoft/hcsshim/ext4/dmverity"
)

// ToolsRecord describes a built dm-verity EROFS tools image (spec 20.1).
type ToolsRecord struct {
	Path       string `json:"path"`
	SHA256     string `json:"sha256"`
	Size       int64  `json:"size"`
	RootHash   string `json:"root_hash"`
	Salt       string `json:"salt"`
	DataBlocks uint64 `json:"data_blocks"`
}

// ToolsBuildOptions configures BuildToolsImage. Zero fields use defaults.
type ToolsBuildOptions struct {
	MkfsErofs string // path to mkfs.erofs; defaults to exec.LookPath("mkfs.erofs")
	ShadowVT  string // optional path to shadow-vt binary installed at /bin/<vtName>
	VTName    string // basename under /bin/ when ShadowVT is set
}

// toolsMountDirs are the empty mountpoint directories in the tools EROFS image
// (isolation spec 20.1).
var toolsMountDirs = []string{
	"bin",
	"dev",
	"mnt",
	"mnt/agents",
	"mnt/image",
	"mnt/root",
	"mnt/session",
	"proc",
	"run",
	"sys",
	"tmp",
}

// BuildToolsImage builds a deterministic read-only EROFS tools image containing
// helperBin at /init and the empty mountpoint directories of spec 20.1, pads
// it to a 4096-byte multiple, and appends a raw SHA-256 dm-verity Merkle tree
// (no verity superblock, 32-zero-byte salt) using hcsshim's dmverity package.
func BuildToolsImage(ctx context.Context, helperBin, outPath string, opts ToolsBuildOptions) (ToolsRecord, error) {
	if helperBin == "" {
		return ToolsRecord{}, errors.New("tools image requires a static Linux helper binary. Try: build ./cmd/host with CGO_ENABLED=0")
	}
	mkfsBin := opts.MkfsErofs
	if mkfsBin == "" {
		var err error
		mkfsBin, err = exec.LookPath("mkfs.erofs")
		if err != nil {
			return ToolsRecord{}, errors.New("mkfs.erofs is not installed. Try: install erofs-utils (for example, sudo apt-get install -y erofs-utils)")
		}
	}

	cleanOut := filepath.Clean(outPath)
	dir := filepath.Dir(cleanOut)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ToolsRecord{}, fmt.Errorf("create tools image folder: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".tools-*.erofs")
	if err != nil {
		return ToolsRecord{}, fmt.Errorf("create temp tools image: %w", err)
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	if err := buildToolsEROFS(ctx, mkfsBin, helperBin, opts.ShadowVT, opts.VTName, tmpPath); err != nil {
		return ToolsRecord{}, err
	}
	dataBlocks, rootHash, totalSize, err := appendRawVerity(tmpPath)
	if err != nil {
		return ToolsRecord{}, err
	}
	hexSHA, err := fileSHA256(tmpPath)
	if err != nil {
		return ToolsRecord{}, err
	}
	//nolint:gosec // G302: read-only tools image, mode 0444 per spec 20.1
	if err := os.Chmod(tmpPath, 0o444); err != nil {
		return ToolsRecord{}, fmt.Errorf("chmod tools image: %w", err)
	}
	if err := os.Rename(tmpPath, cleanOut); err != nil {
		return ToolsRecord{}, fmt.Errorf("install tools image: %w", err)
	}
	cleanup = false
	return ToolsRecord{
		Path:       cleanOut,
		SHA256:     hexSHA,
		Size:       totalSize,
		RootHash:   rootHash,
		Salt:       ZeroSaltHex,
		DataBlocks: dataBlocks,
	}, nil
}

func buildToolsEROFS(ctx context.Context, mkfsBin, helperBin, vtBin, vtName, outPath string) error {
	pr, pw := io.Pipe()
	errCh := make(chan error, 1)
	go func() {
		errCh <- writeToolsTar(pw, helperBin, vtBin, vtName)
	}()

	cmd := exec.CommandContext(ctx, mkfsBin,
		"--tar=f",
		"--all-root",
		"-U", "clear",
		"-T", "0",
		"--mkfs-time",
		"-b", "4096",
		"--quiet",
		outPath,
	)
	cmd.Stdin = pr
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	_ = pr.Close()
	tarErr := <-errCh
	if tarErr != nil {
		return fmt.Errorf("stream tools tar: %w", tarErr)
	}
	if runErr != nil {
		return fmt.Errorf("mkfs.erofs failed (%s): %w. Try: ensure erofs-utils 1.8+ is installed", strings.TrimSpace(stderr.String()), runErr)
	}
	return nil
}

func writeToolsTar(w *io.PipeWriter, helperBin, vtBin, vtName string) error {
	tw := tar.NewWriter(w)
	epoch := time.Unix(0, 0).UTC()

	for _, d := range toolsMountDirs {
		if err := tw.WriteHeader(&tar.Header{
			Typeflag: tar.TypeDir,
			Name:     d,
			Mode:     0o755,
			ModTime:  epoch,
			Format:   tar.FormatPAX,
		}); err != nil {
			_ = w.CloseWithError(err)
			return fmt.Errorf("write dir %s: %w", d, err)
		}
	}
	if err := addFileToTar(tw, "init", helperBin, 0o755, epoch); err != nil {
		_ = w.CloseWithError(err)
		return err
	}
	if vtBin != "" && vtName != "" {
		if err := addFileToTar(tw, "bin/"+vtName, vtBin, 0o755, epoch); err != nil {
			_ = w.CloseWithError(err)
			return err
		}
	}
	if err := tw.Close(); err != nil {
		_ = w.CloseWithError(err)
		return fmt.Errorf("close tools tar: %w", err)
	}
	_ = w.Close()
	return nil
}

func addFileToTar(tw *tar.Writer, tarName, hostPath string, mode int64, modTime time.Time) error {
	f, err := os.Open(filepath.Clean(hostPath))
	if err != nil {
		return fmt.Errorf("open %s: %w", hostPath, err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat %s: %w", hostPath, err)
	}
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg,
		Name:     tarName,
		Size:     fi.Size(),
		Mode:     mode,
		ModTime:  modTime,
		Format:   tar.FormatPAX,
	}); err != nil {
		return fmt.Errorf("write tar header %s: %w", tarName, err)
	}
	if _, err := io.Copy(tw, f); err != nil {
		return fmt.Errorf("copy %s into tar: %w", hostPath, err)
	}
	return nil
}

// appendRawVerity pads path to a 4096-byte boundary and appends a raw SHA-256
// dm-verity Merkle tree (no superblock, 32-zero-byte salt) per spec 20.1.
func appendRawVerity(path string) (dataBlocks uint64, rootHash string, totalSize int64, err error) {
	f, err := os.OpenFile(filepath.Clean(path), os.O_RDWR, 0)
	if err != nil {
		return 0, "", 0, fmt.Errorf("open tools image for verity: %w", err)
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return 0, "", 0, fmt.Errorf("stat tools image: %w", err)
	}
	dataSize := fi.Size()
	if rem := dataSize % verityBlockBytes; rem != 0 {
		pad := make([]byte, verityBlockBytes-rem)
		if _, err := f.Seek(0, io.SeekEnd); err != nil {
			return 0, "", 0, fmt.Errorf("seek tools image end: %w", err)
		}
		if _, err := f.Write(pad); err != nil {
			return 0, "", 0, fmt.Errorf("pad tools image: %w", err)
		}
		dataSize += int64(len(pad))
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return 0, "", 0, fmt.Errorf("rewind tools image: %w", err)
	}

	tree, err := dmverity.MerkleTree(io.LimitReader(f, dataSize))
	if err != nil {
		return 0, "", 0, fmt.Errorf("compute tools dm-verity tree: %w", err)
	}
	if len(tree) < verityBlockBytes {
		return 0, "", 0, fmt.Errorf("tools dm-verity tree unexpectedly short (%d bytes)", len(tree))
	}
	root := dmverity.RootHash(tree[:verityBlockBytes])
	if _, err := f.Seek(dataSize, io.SeekStart); err != nil {
		return 0, "", 0, fmt.Errorf("seek tools verity offset: %w", err)
	}
	if _, err := f.Write(tree); err != nil {
		return 0, "", 0, fmt.Errorf("write tools verity tree: %w", err)
	}
	if err := f.Sync(); err != nil {
		return 0, "", 0, fmt.Errorf("sync tools image: %w", err)
	}
	totalSize = dataSize + int64(len(tree))
	//nolint:gosec // G115: dataSize is non-negative file size
	dataBlocks = uint64(dataSize / verityBlockBytes)
	return dataBlocks, hex.EncodeToString(root), totalSize, nil
}
