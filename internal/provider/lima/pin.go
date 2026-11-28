package lima

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/provider"
)

// PinnedVersion is the exact Lima release pinned for local Mac hosts.
const PinnedVersion = "2.3.0"

// maxArchiveBytes caps the downloaded Lima release archive (Lima 2.3.0 is ~50 MiB).
const maxArchiveBytes = 150 << 20

// maxExtractedBytes caps the uncompressed bytes written when extracting Lima.
const maxExtractedBytes = 512 << 20

// maxArchiveEntries caps the number of files extracted from a Lima archive.
const maxArchiveEntries = 2048

type pinnedArchive struct {
	file   string
	sha256 string
}

// pinnedArchives holds the official SHA256SUMS digests from Lima v2.3.0.
var pinnedArchives = map[string]pinnedArchive{
	"darwin/arm64": {
		file:   "lima-2.3.0-Darwin-arm64.tar.gz",
		sha256: "b7af444bd8f91d3c307477e562f2cf3bd7595d62b9429185e4fa841563578cfc",
	},
	"darwin/amd64": {
		file:   "lima-2.3.0-Darwin-x86_64.tar.gz",
		sha256: "d58bc7d6b8158d638720d8c8fdb17eb58527dfead6a29a5b60f48a35ee4a65a6",
	},
	"linux/arm64": {
		file:   "lima-2.3.0-Linux-aarch64.tar.gz",
		sha256: "7e295d243d9b9439fa05628500daff4371a6861cc7e995ac4af4a0781f736cc6",
	},
	"linux/amd64": {
		file:   "lima-2.3.0-Linux-x86_64.tar.gz",
		sha256: "cc8ba97f1c801b899f7c9eda9919dfc4ce4fa2c140c53e5add15aa2d0a1936f9",
	},
}

// Test hooks for architecture, download URL and HTTP client.
var (
	goarch        = runtime.GOARCH
	pinnedBaseURL = "https://github.com/lima-vm/lima/releases/download/v" + PinnedVersion
	pinnedClient  = &http.Client{Timeout: 5 * time.Minute}
)

// PinnedDir is where this client keeps its pinned Lima installation.
func PinnedDir() string {
	return filepath.Join(paths.DataDir(), "lima", PinnedVersion)
}

// PinnedBinary is the pinned limactl executable inside PinnedDir.
func PinnedBinary() string {
	return filepath.Join(PinnedDir(), "bin", "limactl")
}

// limaCommand returns the pinned limactl path if installed, else "limactl" on PATH.
func limaCommand() string {
	p := PinnedBinary()
	if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
		return p
	}
	return "limactl"
}

// EnsurePinned installs the pinned Lima release into PinnedDir if it is not
// already present, checking the archive's SHA-256 digest before extracting.
// It returns the path to the pinned limactl binary.
func EnsurePinned(ctx context.Context, out io.Writer) (string, error) {
	if out == nil {
		out = io.Discard
	}
	bin := PinnedBinary()
	if fi, err := os.Stat(bin); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
		return bin, nil
	}
	spec, ok := pinnedArchives[goos+"/"+goarch]
	if !ok {
		return "", &client.UserError{
			Msg:  fmt.Sprintf("no pinned Lima %s release for %s/%s", PinnedVersion, goos, goarch),
			Hint: install,
			Code: "lima_unsupported",
		}
	}
	parent := filepath.Dir(PinnedDir())
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", fmt.Errorf("make %s: %w", parent, err)
	}
	url := strings.TrimRight(pinnedBaseURL, "/") + "/" + spec.file
	fmt.Fprintf(out, "  Downloading Lima %s (%s)…\n", PinnedVersion, spec.file)
	tgz, err := downloadChecked(ctx, parent, url, spec.sha256)
	if err != nil {
		return "", err
	}
	defer os.Remove(tgz)

	stage, err := os.MkdirTemp(parent, "lima-stage-*")
	if err != nil {
		return "", fmt.Errorf("make staging folder: %w", err)
	}
	defer os.RemoveAll(stage)

	if err := extractTarGz(tgz, stage); err != nil {
		return "", fmt.Errorf("extract Lima %s: %w", PinnedVersion, err)
	}
	stagedBin := filepath.Join(stage, "bin", "limactl")
	if fi, err := os.Stat(stagedBin); err != nil || fi.IsDir() || fi.Mode()&0o111 == 0 {
		return "", fmt.Errorf("the Lima %s archive has no executable bin/limactl", PinnedVersion)
	}
	if err := verifyPinnedBinary(ctx, stagedBin); err != nil {
		return "", err
	}
	dst := PinnedDir()
	if err := os.Rename(stage, dst); err != nil {
		// Another process may have installed the same pinned version meanwhile.
		if fi, serr := os.Stat(bin); serr == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return bin, nil
		}
		return "", fmt.Errorf("install Lima %s into %s: %w", PinnedVersion, dst, err)
	}
	fmt.Fprintf(out, "  ✓ Lima %s ready (SHA-256 verified) at %s\n", PinnedVersion, dst)
	return bin, nil
}

func downloadChecked(ctx context.Context, dir, url, wantSHA string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("build Lima download request: %w", err)
	}
	resp, err := pinnedClient.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return "", fmt.Errorf("download Lima %s: %w", PinnedVersion, ctx.Err())
		}
		return "", &client.UserError{
			Msg:  fmt.Sprintf("could not download Lima %s: %v", PinnedVersion, err),
			Hint: "Check your internet connection and try again, or install Lima " + PinnedVersion + " from https://lima-vm.io/docs/installation/.",
			Code: "lima_download",
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", &client.UserError{
			Msg:  fmt.Sprintf("downloading Lima %s failed with HTTP %d", PinnedVersion, resp.StatusCode),
			Hint: "Try again later, or install Lima " + PinnedVersion + " from https://lima-vm.io/docs/installation/.",
			Code: "lima_download",
		}
	}
	f, err := os.CreateTemp(dir, "lima-*.tgz.part")
	if err != nil {
		return "", fmt.Errorf("make temporary archive: %w", err)
	}
	tmpPath := f.Name()
	keep := false
	defer func() {
		_ = f.Close()
		if !keep {
			_ = os.Remove(tmpPath)
		}
	}()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, maxArchiveBytes+1))
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return "", fmt.Errorf("read Lima %s archive: %w", PinnedVersion, ctx.Err())
		}
		return "", &client.UserError{
			Msg:  fmt.Sprintf("could not download Lima %s: %v", PinnedVersion, err),
			Hint: "Check your internet connection and try again, or install Lima " + PinnedVersion + " from https://lima-vm.io/docs/installation/.",
			Code: "lima_download",
		}
	}
	if n > maxArchiveBytes {
		return "", &client.UserError{
			Msg:  fmt.Sprintf("Lima %s archive is larger than %d MiB", PinnedVersion, maxArchiveBytes>>20),
			Hint: "Check the download source and try again.",
			Code: "lima_digest",
		}
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("close temporary archive: %w", err)
	}
	gotSHA := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(gotSHA, wantSHA) {
		return "", &client.UserError{
			Msg:  fmt.Sprintf("Lima %s archive SHA-256 did not match: got %s, want %s", PinnedVersion, gotSHA, wantSHA),
			Hint: "Do not use this download; check your network or try again later.",
			Code: "lima_digest",
		}
	}
	keep = true
	return tmpPath, nil
}

func extractTarGz(archivePath, dst string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("open gzip stream: %w", err)
	}
	defer gz.Close()

	absDst, err := filepath.Abs(dst)
	if err != nil {
		return fmt.Errorf("resolve staging folder: %w", err)
	}
	root, err := os.OpenRoot(absDst)
	if err != nil {
		return fmt.Errorf("open staging folder: %w", err)
	}
	defer root.Close()
	tr := tar.NewReader(gz)
	var totalBytes int64
	entries := 0
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read tar entry: %w", err)
		}
		entries++
		if entries > maxArchiveEntries {
			return fmt.Errorf("archive has more than %d entries", maxArchiveEntries)
		}
		rel, err := safeRelPath(hdr.Name)
		if err != nil {
			return err
		}
		if rel == "." {
			continue
		}
		written, err := extractEntry(tr, hdr, root, rel, maxExtractedBytes-totalBytes)
		if err != nil {
			return err
		}
		totalBytes += written
	}
}

func safeRelPath(name string) (string, error) {
	if strings.HasPrefix(name, "/") || strings.HasPrefix(name, `\`) {
		return "", fmt.Errorf("archive entry %q uses an absolute path", name)
	}
	clean := filepath.Clean(name)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || filepath.IsAbs(clean) {
		return "", fmt.Errorf("archive entry %q escapes its folder", name)
	}
	return clean, nil
}

func rejectSymlinkComponents(root *os.Root, rel string) error {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	cur := ""
	for _, p := range parts {
		if cur == "" {
			cur = p
		} else {
			cur += "/" + p
		}
		fi, err := root.Lstat(cur)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("stat %s: %w", cur, err)
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("archive entry %q traverses symlink %q", rel, cur)
		}
	}
	return nil
}

func extractEntry(r io.Reader, hdr *tar.Header, root *os.Root, rel string, remaining int64) (int64, error) {
	switch hdr.Typeflag {
	case tar.TypeDir:
		if err := rejectSymlinkComponents(root, rel); err != nil {
			return 0, err
		}
		if err := root.MkdirAll(rel, 0o750); err != nil {
			return 0, fmt.Errorf("make directory %s: %w", hdr.Name, err)
		}
		return 0, nil
	case tar.TypeReg:
		return extractReg(r, hdr, root, rel, remaining)
	case tar.TypeSymlink:
		return 0, extractSymlink(hdr, root, rel)
	case tar.TypeXHeader, tar.TypeXGlobalHeader, tar.TypeGNULongName, tar.TypeGNULongLink:
		return 0, nil
	default:
		return 0, fmt.Errorf("unsupported tar entry type %d for %q", hdr.Typeflag, hdr.Name)
	}
}

func extractReg(r io.Reader, hdr *tar.Header, root *os.Root, rel string, remaining int64) (int64, error) {
	if err := rejectSymlinkComponents(root, rel); err != nil {
		return 0, err
	}
	if parent := filepath.Dir(rel); parent != "." {
		if err := root.MkdirAll(parent, 0o750); err != nil {
			return 0, fmt.Errorf("make parent for %s: %w", hdr.Name, err)
		}
	}
	if hdr.Size < 0 || hdr.Size > remaining {
		return 0, fmt.Errorf("archive exceeds %d MiB extracted limit", maxExtractedBytes>>20)
	}
	mode := os.FileMode(hdr.Mode & 0o755)
	if mode == 0 {
		mode = 0o644
	}
	out, err := root.OpenFile(rel, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return 0, fmt.Errorf("create %s: %w", hdr.Name, err)
	}
	n, copyErr := io.Copy(out, io.LimitReader(r, remaining+1))
	closeErr := out.Close()
	if copyErr != nil {
		return 0, fmt.Errorf("write %s: %w", hdr.Name, copyErr)
	}
	if closeErr != nil {
		return 0, fmt.Errorf("close %s: %w", hdr.Name, closeErr)
	}
	if n > remaining {
		return 0, fmt.Errorf("archive exceeds %d MiB extracted limit", maxExtractedBytes>>20)
	}
	return n, nil
}

func extractSymlink(hdr *tar.Header, root *os.Root, rel string) error {
	if filepath.IsAbs(hdr.Linkname) || strings.HasPrefix(hdr.Linkname, `\`) {
		return fmt.Errorf("symlink %q points to absolute path %q", hdr.Name, hdr.Linkname)
	}
	if err := rejectSymlinkComponents(root, rel); err != nil {
		return err
	}
	resolved := filepath.Clean(filepath.Join(filepath.Dir(rel), hdr.Linkname)) //nolint:gosec // G305: containment inside os.Root and symlink-parent checks are enforced
	if resolved == ".." || strings.HasPrefix(resolved, ".."+string(filepath.Separator)) || filepath.IsAbs(resolved) {
		return fmt.Errorf("symlink %q escapes staging folder", hdr.Name)
	}
	if parent := filepath.Dir(rel); parent != "." {
		if err := root.MkdirAll(parent, 0o750); err != nil {
			return fmt.Errorf("make parent for symlink %s: %w", hdr.Name, err)
		}
	}
	if err := root.Symlink(hdr.Linkname, rel); err != nil {
		return fmt.Errorf("create symlink %s: %w", hdr.Name, err)
	}
	return nil
}

func verifyPinnedBinary(ctx context.Context, bin string) error {
	out, err := provider.Exec(ctx, install, bin, "--version")
	if err != nil {
		return fmt.Errorf("check pinned limactl: %w", err)
	}
	v, err := parseLimaVersion(strings.TrimSpace(string(out)))
	if err != nil {
		return fmt.Errorf("read pinned limactl version: %w", err)
	}
	if v.before(minVersion) {
		return tooOld(v.text, goos)
	}
	return nil
}
