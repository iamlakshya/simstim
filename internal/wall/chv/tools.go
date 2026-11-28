package chv

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Microsoft/hcsshim/ext4/tar2ext4"

	"github.com/iamlakshya/simstim/internal/supply"
)

const maxToolsExt4Bytes = 256 << 20 // 256 MiB ceiling for fallback ext4 tools image

// ToolsRecord describes a built dm-verity tools image (spec 20.1).
type ToolsRecord struct {
	Path         string `json:"path"`
	SHA256       string `json:"sha256"`
	Size         int64  `json:"size"`
	RootHash     string `json:"root_hash"`
	Salt         string `json:"salt"`
	DataBlocks   uint64 `json:"data_blocks"`
	FSType       string `json:"fs_type,omitempty"`
	HelperSHA256 string `json:"helper_sha256,omitempty"`
}

// ToolsBuildOptions configures BuildToolsImage. Zero fields use defaults.
type ToolsBuildOptions struct {
	MkfsErofs         string // path to mkfs.erofs; defaults to exec.LookPath("mkfs.erofs")
	AllowExt4Fallback bool   // when true and mkfs.erofs is absent, build ext4 via pure-Go hcsshim tar2ext4
	ShadowVT          string // optional path to shadow-vt binary installed at /bin/<vtName>
	VTName            string // basename under /bin/ when ShadowVT is set
	Epoch             int64  // SOURCE_DATE_EPOCH timestamp for deterministic filesystem metadata
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

// BuildToolsImage builds a deterministic read-only EROFS (or ext4 fallback via
// pure-Go hcsshim tar2ext4 when opts.AllowExt4Fallback is true and mkfs.erofs
// is absent) tools image containing helperBin at /init and the empty mountpoint
// directories of spec 20.1, pads it to a 4096-byte multiple, and appends a raw
// SHA-256 dm-verity Merkle tree (no verity superblock, 32-zero-byte salt).
func BuildToolsImage(ctx context.Context, helperBin, outPath string, opts ToolsBuildOptions) (ToolsRecord, error) {
	if helperBin == "" {
		return ToolsRecord{}, errors.New("tools image requires a static Linux helper binary. Try: build ./cmd/host with CGO_ENABLED=0")
	}
	helperSHA, err := fileSHA256(helperBin)
	if err != nil {
		return ToolsRecord{}, fmt.Errorf("hash helper binary: %w. Try: pass an existing static host helper binary", err)
	}
	mkfsBin := opts.MkfsErofs
	fsType := "erofs"
	if mkfsBin == "" {
		var lookErr error
		mkfsBin, lookErr = exec.LookPath("mkfs.erofs")
		if lookErr != nil {
			if !opts.AllowExt4Fallback {
				return ToolsRecord{}, errors.New("mkfs.erofs is not installed. Try: install erofs-utils (for example, sudo apt-get install -y erofs-utils)")
			}
			fsType = "ext4"
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

	if fsType == "ext4" {
		if err := buildToolsExt4(ctx, helperBin, opts.ShadowVT, opts.VTName, opts.Epoch, tmpPath); err != nil {
			return ToolsRecord{}, err
		}
	} else {
		if err := buildToolsEROFS(ctx, mkfsBin, helperBin, opts.ShadowVT, opts.VTName, opts.Epoch, tmpPath); err != nil {
			return ToolsRecord{}, err
		}
	}
	vi, err := supply.AppendRawVerity(tmpPath)
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
		Path:         cleanOut,
		SHA256:       vi.SHA256,
		Size:         vi.Size,
		RootHash:     vi.RootHash,
		Salt:         vi.Salt,
		DataBlocks:   vi.DataBlocks,
		FSType:       fsType,
		HelperSHA256: helperSHA,
	}, nil
}

// EnsureToolsImage verifies or builds outPath and its sidecar metadata file
// (outPath + ".json") for helperBin. An existing tools image is reused only
// when its recorded helper digest, file SHA-256 and appended dm-verity Merkle
// tree all verify cleanly; otherwise it is rebuilt atomically.
func EnsureToolsImage(ctx context.Context, helperBin, outPath string, opts ToolsBuildOptions) (ToolsRecord, error) {
	helperSHA, err := fileSHA256(helperBin)
	if err != nil {
		return ToolsRecord{}, fmt.Errorf("hash helper binary: %w. Try: check the host helper installation", err)
	}
	cleanOut := filepath.Clean(outPath)
	metaPath := cleanOut + ".json"
	if rec, ok := loadVerifiedToolsRecord(cleanOut, metaPath, helperSHA); ok {
		return rec, nil
	}
	rec, err := BuildToolsImage(ctx, helperBin, cleanOut, opts)
	if err != nil {
		return ToolsRecord{}, err
	}
	if err := writeJSONAtomic(metaPath, rec); err != nil {
		return ToolsRecord{}, fmt.Errorf("write tools image metadata: %w", err)
	}
	return rec, nil
}

func loadVerifiedToolsRecord(toolsPath, metaPath, wantHelperSHA string) (ToolsRecord, bool) {
	b, err := os.ReadFile(filepath.Clean(metaPath))
	if err != nil {
		return ToolsRecord{}, false
	}
	var rec ToolsRecord
	if json.Unmarshal(b, &rec) != nil {
		return ToolsRecord{}, false
	}
	if rec.RootHash == "" || rec.DataBlocks == 0 || rec.SHA256 == "" || rec.HelperSHA256 == "" {
		return ToolsRecord{}, false
	}
	if !strings.EqualFold(rec.HelperSHA256, wantHelperSHA) {
		return ToolsRecord{}, false
	}
	vi, err := supply.VerifyRawVerity(toolsPath, rec.DataBlocks, rec.RootHash)
	if err != nil {
		return ToolsRecord{}, false
	}
	if !strings.EqualFold(vi.SHA256, rec.SHA256) {
		return ToolsRecord{}, false
	}
	rec.Path = toolsPath
	return rec, true
}

func buildToolsExt4(ctx context.Context, helperBin, vtBin, vtName string, epoch int64, outPath string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("build ext4 tools image: %w", err)
	}
	f, err := os.OpenFile(filepath.Clean(outPath), os.O_RDWR|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open temp ext4 tools image: %w", err)
	}
	pr, pw := io.Pipe()
	errCh := make(chan error, 1)
	go func() {
		errCh <- writeToolsTar(pw, helperBin, vtBin, vtName, epoch)
	}()

	convErr := tar2ext4.Convert(pr, f, tar2ext4.MaximumDiskSize(maxToolsExt4Bytes))
	_ = pr.Close()
	tarErr := <-errCh
	syncErr := f.Sync()
	closeErr := f.Close()

	if tarErr != nil {
		return fmt.Errorf("stream tools tar: %w", tarErr)
	}
	if convErr != nil {
		return fmt.Errorf("convert tools image to ext4: %w. Try: install erofs-utils or retry wall install microvm", convErr)
	}
	if syncErr != nil {
		return fmt.Errorf("sync ext4 tools image: %w", syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close ext4 tools image: %w", closeErr)
	}
	return nil
}

func writeJSONAtomic(dst string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("encode json: %w", err)
	}
	b = append(b, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".tmp-*.json")
	if err != nil {
		return fmt.Errorf("create temp json: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write temp json: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("sync temp json: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close temp json: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("chmod temp json: %w", err)
	}
	if err := os.Rename(tmpPath, dst); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("install json %s: %w", dst, err)
	}
	return nil
}

func buildToolsEROFS(ctx context.Context, mkfsBin, helperBin, vtBin, vtName string, epoch int64, outPath string) error {
	pr, pw := io.Pipe()
	errCh := make(chan error, 1)
	go func() {
		errCh <- writeToolsTar(pw, helperBin, vtBin, vtName, epoch)
	}()

	cmd := exec.CommandContext(ctx, mkfsBin,
		"--tar=f",
		"--all-root",
		"-U", "clear",
		"-T", strconv.FormatInt(epoch, 10),
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

func writeToolsTar(w *io.PipeWriter, helperBin, vtBin, vtName string, epochSec int64) error {
	tw := tar.NewWriter(w)
	epoch := time.Unix(epochSec, 0).UTC()

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
