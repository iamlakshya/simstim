package client

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
)

// allowInsecureClientRuntimeURL lets unit tests serve runtime files over local
// HTTP without TLS. Production always requires HTTPS.
var allowInsecureClientRuntimeURL bool

// RuntimeCacheDir is the laptop's content-addressed runtime cache folder:
// ~/.cache/<name>/runtime/sha256 (isolation spec 14.9.3).
func RuntimeCacheDir() string {
	return filepath.Join(CacheDir(), "runtime", "sha256")
}

func deliverRuntimeViaLaptop(
	ctx context.Context,
	conn *Conn,
	manifest api.RuntimeManifest,
	missing, allItems []api.RuntimeAsset,
	opts EnsureRuntimeOptions,
) (EnsureRuntimeResult, error) {
	tr := opts.Transport
	if tr == nil {
		var err error
		tr, err = HostTransport(ctx, conn.Host, opts.Interactive)
		if err != nil {
			return EnsureRuntimeResult{}, err
		}
	}
	cacheDir := opts.CacheDir
	if cacheDir == "" {
		cacheDir = RuntimeCacheDir()
	}
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return EnsureRuntimeResult{}, fmt.Errorf("create runtime cache directory: %w", err)
	}
	hc := opts.HTTPClient
	if hc == nil {
		hc = defaultLaptopRuntimeHTTPClient()
	}

	installed := make([]api.RuntimeInstalledFile, 0, len(missing))
	for i, item := range missing {
		start := time.Now()
		localPath, err := ensureLaptopCachedRuntimeFile(ctx, hc, cacheDir, item)
		if err != nil {
			return EnsureRuntimeResult{}, err
		}
		partName, err := uploadRuntimePart(ctx, tr, localPath, item)
		if err != nil {
			return EnsureRuntimeResult{}, err
		}
		req := api.RuntimeInstallArgs{
			Items: []api.RuntimeInstallEntry{{Asset: item, Part: partName}},
		}
		if i == len(missing)-1 {
			req.Release = manifest.Release
			req.Runtime = manifest.Runtime
			req.LabelItems = allItems
		}
		res, err := conn.RuntimeInstall(ctx, req)
		if err != nil {
			return EnsureRuntimeResult{}, err
		}
		elapsed := time.Since(start)
		for _, f := range res.Installed {
			f.DurationMS = elapsed.Milliseconds()
			installed = append(installed, f)
			if opts.Out != nil {
				writeRuntimeProgressLine(opts.Out, f.Name, f.Size, elapsed)
			}
		}
	}
	return EnsureRuntimeResult{
		Mode:      api.ImagesFetchLaptop,
		Runtime:   manifest.Runtime,
		Installed: installed,
	}, nil
}

func defaultLaptopRuntimeHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 10 * time.Minute,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if !allowInsecureClientRuntimeURL && !strings.EqualFold(req.URL.Scheme, "https") {
				return fmt.Errorf("refusing non-HTTPS redirect to %s", req.URL)
			}
			if len(via) >= 10 {
				return errors.New("too many redirects while downloading runtime file")
			}
			return nil
		},
	}
}

func ensureLaptopCachedRuntimeFile(ctx context.Context, hc *http.Client, cacheDir string, item api.RuntimeAsset) (string, error) {
	dst := filepath.Join(cacheDir, item.SHA256)
	if verifyLocalRuntimeFile(cacheDir, item.SHA256, item.Size) {
		return dst, nil
	}
	u, err := url.Parse(strings.TrimSpace(item.URL))
	if err != nil || u.Host == "" {
		return "", &UserError{
			Msg:  fmt.Sprintf("invalid download URL for runtime item %q", item.Name),
			Hint: "Update the client.",
			Code: "runtime_fetch_failed",
		}
	}
	if !allowInsecureClientRuntimeURL && !strings.EqualFold(u.Scheme, "https") {
		return "", &UserError{
			Msg:  fmt.Sprintf("runtime download URL for %q must use HTTPS", item.Name),
			Hint: "Use an HTTPS release mirror.",
			Code: "runtime_fetch_failed",
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, item.URL, nil)
	if err != nil {
		return "", &UserError{
			Msg:  fmt.Sprintf("could not download %s: %v", item.Name, err),
			Hint: "Check your network connection and retry.",
			Code: "runtime_fetch_failed",
		}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", &UserError{
			Msg:  fmt.Sprintf("could not download %s: %v", item.Name, err),
			Hint: "Check your network connection and retry.",
			Code: "runtime_fetch_failed",
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", &UserError{
			Msg:  fmt.Sprintf("could not download %s: HTTP %d", item.Name, resp.StatusCode),
			Hint: "Check your network connection and retry.",
			Code: "runtime_fetch_failed",
		}
	}
	if resp.ContentLength > item.Size {
		return "", &UserError{
			Msg:  fmt.Sprintf("%s is larger than its manifest size (%d > %d bytes)", item.Name, resp.ContentLength, item.Size),
			Hint: "Check the release mirror or update the client.",
			Code: "runtime_too_big",
		}
	}

	partName := item.SHA256 + "." + randomRuntimeHex(8) + ".part"
	partPath := filepath.Join(cacheDir, partName)
	f, err := os.OpenFile(partPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", fmt.Errorf("create cached runtime part: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(partPath)
		}
	}()

	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, item.Size+1))
	closeErr := f.Close()
	if copyErr != nil {
		return "", &UserError{
			Msg:  fmt.Sprintf("download of %s stopped: %v", item.Name, copyErr),
			Hint: "Check your network connection and retry.",
			Code: "runtime_fetch_failed",
		}
	}
	if closeErr != nil {
		return "", fmt.Errorf("close cached runtime part: %w", closeErr)
	}
	if n > item.Size {
		return "", &UserError{
			Msg:  fmt.Sprintf("%s exceeded its manifest size of %d bytes", item.Name, item.Size),
			Hint: "Check the release mirror or update the client.",
			Code: "runtime_too_big",
		}
	}
	if n != item.Size {
		return "", &UserError{
			Msg:  fmt.Sprintf("%s download was cut short (%d of %d bytes)", item.Name, n, item.Size),
			Hint: "Retry the download.",
			Code: "digest_mismatch",
		}
	}
	gotHex := hex.EncodeToString(h.Sum(nil))
	if gotHex != item.SHA256 {
		return "", &UserError{
			Msg:  fmt.Sprintf("%s does not match its manifest SHA-256 (got %s, want %s)", item.Name, gotHex[:12], item.SHA256[:12]),
			Hint: "Check the release mirror or update the client.",
			Code: "digest_mismatch",
		}
	}
	if err := os.Chmod(partPath, 0o400); err != nil {
		return "", fmt.Errorf("chmod cached runtime file: %w", err)
	}
	if err := os.Link(partPath, dst); err == nil {
		_ = os.Remove(partPath)
	} else if errors.Is(err, os.ErrExist) && verifyLocalRuntimeFile(cacheDir, item.SHA256, item.Size) {
		_ = os.Remove(partPath)
	} else if err := os.Rename(partPath, dst); err != nil {
		return "", fmt.Errorf("store cached runtime file: %w", err)
	}
	cleanup = false
	return dst, nil
}

func verifyLocalRuntimeFile(cacheDir, wantHex string, wantSize int64) bool {
	root, err := os.OpenRoot(cacheDir)
	if err != nil {
		return false
	}
	defer func() { _ = root.Close() }()
	fi, err := root.Lstat(wantHex)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() != wantSize {
		return false
	}
	f, err := root.Open(wantHex)
	if err != nil {
		return false
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, wantSize+1))
	if err != nil || n != wantSize {
		return false
	}
	return hex.EncodeToString(h.Sum(nil)) == wantHex
}

func uploadRuntimePart(ctx context.Context, tr Transport, localPath string, item api.RuntimeAsset) (string, error) {
	f, err := os.Open(localPath)
	if err != nil {
		return "", fmt.Errorf("open cached runtime file %s: %w", item.Name, err)
	}
	defer f.Close()

	partName := item.SHA256 + "." + randomRuntimeHex(8) + ".part"
	steps := []string{
		"set -e",
		"umask 077",
		`case "${XDG_DATA_HOME:-}" in /*) base="$XDG_DATA_HOME" ;; *) base="$HOME/.local/share" ;; esac`,
		fmt.Sprintf(`d="$base/%s/artefacts/incoming"`, brand.Name),
		`mkdir -p "$d"`,
		fmt.Sprintf(`p="$d/%s"`, partName),
		`cat > "$p" || { rm -f "$p"; exit 1; }`,
		`echo __RUNTIME_UPLOADED__`,
	}
	out, err := tr.Run(ctx, strings.Join(steps, "; "), io.LimitReader(f, item.Size))
	if err != nil {
		return "", &UserError{
			Msg:  fmt.Sprintf("could not copy %s to the host: %v", item.Name, err),
			Hint: tr.ReachHint(),
			Code: "runtime_fetch_failed",
		}
	}
	if !strings.Contains(string(out), "__RUNTIME_UPLOADED__") {
		return "", &UserError{
			Msg:  fmt.Sprintf("copy of %s to the host did not finish: %s", item.Name, tailStderr(string(out))),
			Hint: tr.ReachHint(),
			Code: "runtime_fetch_failed",
		}
	}
	return partName, nil
}

func writeRuntimeProgressLine(w io.Writer, name string, size int64, dur time.Duration) {
	fmt.Fprintf(w, "  ✓ %s (%s, %s, sha256 ok)\n",
		CleanLine(name), formatRuntimeBytes(size), dur.Round(time.Millisecond))
}

func formatRuntimeBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/1e6)
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/1e3)
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func randomRuntimeHex(nBytes int) string {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
