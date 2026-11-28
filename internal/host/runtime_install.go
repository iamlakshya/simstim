package host

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
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/paths"
)

const (
	// maxRuntimeAssetBytes caps any single runtime artefact to guard against
	// corrupted or hostile manifests.
	maxRuntimeAssetBytes = 2 << 30 // 2 GiB
)

// runtimeHTTPClient performs runtime.fetch HTTPS downloads. Tests replace it.
var runtimeHTTPClient = func() *http.Client {
	return &http.Client{
		Timeout: 10 * time.Minute,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if !allowInsecureRuntimeURL && !strings.EqualFold(req.URL.Scheme, "https") {
				return fmt.Errorf("refusing non-HTTPS redirect to %s", req.URL)
			}
			if len(via) >= 10 {
				return errors.New("too many redirects while downloading runtime file")
			}
			return nil
		},
	}
}

// allowInsecureRuntimeURL lets unit tests serve runtime files over local HTTP.
var allowInsecureRuntimeURL bool

type activeRuntimeTransfer struct {
	id     string
	sha256 string
	name   string
	size   int64
	bytes  atomic.Int64
}

var (
	runtimeTransfersMu sync.Mutex
	runtimeTransfers   = map[string]*activeRuntimeTransfer{}
)

func registerTransfer(shaHex, name string, size int64) *activeRuntimeTransfer {
	t := &activeRuntimeTransfer{
		id:     randomHex(8),
		sha256: shaHex,
		name:   name,
		size:   size,
	}
	runtimeTransfersMu.Lock()
	runtimeTransfers[t.id] = t
	runtimeTransfersMu.Unlock()
	return t
}

func unregisterTransfer(t *activeRuntimeTransfer) {
	if t == nil {
		return
	}
	runtimeTransfersMu.Lock()
	delete(runtimeTransfers, t.id)
	runtimeTransfersMu.Unlock()
}

type progressWriter struct {
	t *activeRuntimeTransfer
}

func (w progressWriter) Write(p []byte) (int, error) {
	if w.t != nil {
		w.t.bytes.Add(int64(len(p)))
	}
	return len(p), nil
}

// RuntimeStatusReport returns the runtime labels installed on this host, the
// content-addressed files present in artefacts/sha256/, and any live
// runtime.fetch downloads in progress (isolation spec 14.9.3).
func RuntimeStatusReport() api.RuntimeStatusResult {
	labels := readStoredRuntimeLabels()
	runtimes := make([]string, 0, len(labels))
	for _, l := range labels {
		runtimes = append(runtimes, l.Runtime)
	}

	files := map[string]int64{}
	if entries, err := os.ReadDir(paths.ArtefactsSHA256Dir()); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			hexDigest, ok := api.NormalizeSHA256Hex(e.Name())
			if !ok || hexDigest != e.Name() {
				continue
			}
			fi, err := os.Lstat(paths.ArtefactFile(hexDigest))
			if err != nil || !fi.Mode().IsRegular() {
				continue
			}
			files[hexDigest] = fi.Size()
		}
	}

	runtimeTransfersMu.Lock()
	active := make([]api.RuntimeTransfer, 0, len(runtimeTransfers))
	for _, tr := range runtimeTransfers {
		active = append(active, api.RuntimeTransfer{
			SHA256: tr.sha256,
			Name:   tr.name,
			Bytes:  tr.bytes.Load(),
			Size:   tr.size,
		})
	}
	runtimeTransfersMu.Unlock()
	sort.Slice(active, func(i, j int) bool { return active[i].SHA256 < active[j].SHA256 })

	return api.RuntimeStatusResult{
		Runtimes: runtimes,
		Files:    files,
		Active:   active,
	}
}

// FetchRuntime downloads each missing runtime asset from its HTTPS URL into a
// unique part file under artefacts/incoming/, enforces the exact size ceiling,
// verifies its SHA-256, and moves it atomically into artefacts/sha256/<hex>
// with mode 0444 (or 0555 for VMM and runsc) (isolation spec 14.5 and 14.9.3).
func FetchRuntime(ctx context.Context, args api.RuntimeFetchArgs) (api.RuntimeInstallResult, error) {
	if args.Runtime != "" && !api.ValidRuntimeLabel(args.Runtime) {
		return api.RuntimeInstallResult{}, UserError("bad_request", fmt.Sprintf("invalid runtime label %q", args.Runtime), "Update the client.")
	}
	if err := ensureArtefactDirs(); err != nil {
		return api.RuntimeInstallResult{}, err
	}

	res := api.RuntimeInstallResult{
		Runtime:   args.Runtime,
		Installed: make([]api.RuntimeInstalledFile, 0, len(args.Items)),
	}
	assets := make([]api.RuntimeAsset, 0, len(args.Items))
	hc := runtimeHTTPClient()
	for _, raw := range args.Items {
		item, err := validateRuntimeAsset(raw, true)
		if err != nil {
			return api.RuntimeInstallResult{}, err
		}
		rec, err := fetchOneRuntimeAsset(ctx, hc, item)
		if err != nil {
			return api.RuntimeInstallResult{}, err
		}
		res.Installed = append(res.Installed, rec)
		assets = append(assets, item)
	}
	if args.Runtime != "" {
		if err := recordRuntimeLabel(args.Runtime, args.Release, assets, args.LabelItems); err != nil {
			return api.RuntimeInstallResult{}, err
		}
	}
	return res, nil
}

// InstallRuntime verifies part files uploaded by the laptop into
// artefacts/incoming/ against their expected size and SHA-256 and installs each
// atomically into artefacts/sha256/<hex> (isolation spec 14.5 and 14.9.3).
func InstallRuntime(args api.RuntimeInstallArgs) (api.RuntimeInstallResult, error) {
	if args.Runtime != "" && !api.ValidRuntimeLabel(args.Runtime) {
		return api.RuntimeInstallResult{}, UserError("bad_request", fmt.Sprintf("invalid runtime label %q", args.Runtime), "Update the client.")
	}
	if err := ensureArtefactDirs(); err != nil {
		return api.RuntimeInstallResult{}, err
	}

	res := api.RuntimeInstallResult{
		Runtime:   args.Runtime,
		Installed: make([]api.RuntimeInstalledFile, 0, len(args.Items)),
	}
	assets := make([]api.RuntimeAsset, 0, len(args.Items))
	for _, entry := range args.Items {
		item, err := validateRuntimeAsset(entry.Asset, false)
		if err != nil {
			return api.RuntimeInstallResult{}, err
		}
		rec, err := installOneRuntimePart(item, entry.Part)
		if err != nil {
			return api.RuntimeInstallResult{}, err
		}
		res.Installed = append(res.Installed, rec)
		assets = append(assets, item)
	}
	if args.Runtime != "" {
		if err := recordRuntimeLabel(args.Runtime, args.Release, assets, args.LabelItems); err != nil {
			return api.RuntimeInstallResult{}, err
		}
	}
	return res, nil
}

func ensureArtefactDirs() error {
	for _, dir := range []string{
		paths.ArtefactsDir(),
		paths.ArtefactsIncomingDir(),
		paths.ArtefactsSHA256Dir(),
		paths.ArtefactsRuntimeDir(),
	} {
		if err := paths.EnsurePrivateDir(dir); err != nil {
			return err
		}
	}
	return nil
}

func validateRuntimeAsset(raw api.RuntimeAsset, needURL bool) (api.RuntimeAsset, error) {
	normHex, ok := api.NormalizeSHA256Hex(raw.SHA256)
	if !ok {
		return api.RuntimeAsset{}, UserError("bad_request", fmt.Sprintf("invalid SHA-256 digest for runtime item %q", raw.Name), "Update the client.")
	}
	if raw.Size <= 0 || raw.Size > maxRuntimeAssetBytes {
		return api.RuntimeAsset{}, UserError("bad_request", fmt.Sprintf("invalid size %d for runtime item %q", raw.Size, raw.Name), "Update the client.")
	}
	if needURL {
		u, err := url.Parse(strings.TrimSpace(raw.URL))
		if err != nil || u.Host == "" {
			return api.RuntimeAsset{}, UserError("bad_request", fmt.Sprintf("invalid download URL for runtime item %q", raw.Name), "Update the client.")
		}
		if !allowInsecureRuntimeURL && !strings.EqualFold(u.Scheme, "https") {
			return api.RuntimeAsset{}, UserError("bad_request", fmt.Sprintf("runtime download URL for %q must use HTTPS", raw.Name), "Use an HTTPS release URL or --images laptop.")
		}
	}
	raw.SHA256 = normHex
	return raw, nil
}

func fetchOneRuntimeAsset(ctx context.Context, hc *http.Client, item api.RuntimeAsset) (api.RuntimeInstalledFile, error) {
	dst := paths.ArtefactFile(item.SHA256)
	mode := runtimeFileMode(item)
	if verifyExistingArtefact(dst, item.SHA256, item.Size) {
		_ = os.Chmod(dst, mode)
		return api.RuntimeInstalledFile{
			Kind:           item.Kind,
			Name:           item.Name,
			SHA256:         item.SHA256,
			Size:           item.Size,
			Path:           dst,
			AlreadyPresent: true,
		}, nil
	}

	start := time.Now()
	tr := registerTransfer(item.SHA256, item.Name, item.Size)
	defer unregisterTransfer(tr)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, item.URL, nil)
	if err != nil {
		return api.RuntimeInstalledFile{}, UserError("runtime_fetch_failed", fmt.Sprintf("could not download %s: %v", item.Name, err), "Check the release URL or retry with --images laptop.")
	}
	resp, err := hc.Do(req)
	if err != nil {
		return api.RuntimeInstalledFile{}, UserError("runtime_fetch_failed", fmt.Sprintf("could not download %s: %v", item.Name, err), "Check host network access or retry with --images laptop.")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return api.RuntimeInstalledFile{}, UserError("runtime_fetch_failed", fmt.Sprintf("could not download %s: HTTP %d", item.Name, resp.StatusCode), "Check host network access or retry with --images laptop.")
	}
	if resp.ContentLength > item.Size {
		return api.RuntimeInstalledFile{}, UserError("runtime_too_big", fmt.Sprintf("%s is larger than its manifest size (%d > %d bytes)", item.Name, resp.ContentLength, item.Size), "Retry with --images laptop or check the release manifest.")
	}

	partPath := filepath.Join(paths.ArtefactsIncomingDir(), item.SHA256+"."+randomHex(8)+".part")
	f, err := os.OpenFile(partPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return api.RuntimeInstalledFile{}, fmt.Errorf("create incoming part file: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(partPath)
		}
	}()

	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, h, progressWriter{t: tr}), io.LimitReader(resp.Body, item.Size+1))
	closeErr := f.Close()
	if copyErr != nil {
		return api.RuntimeInstalledFile{}, UserError("runtime_fetch_failed", fmt.Sprintf("download of %s stopped: %v", item.Name, copyErr), "Retry or pass --images laptop.")
	}
	if closeErr != nil {
		return api.RuntimeInstalledFile{}, fmt.Errorf("close part file: %w", closeErr)
	}
	if n > item.Size {
		return api.RuntimeInstalledFile{}, UserError("runtime_too_big", fmt.Sprintf("%s exceeded its manifest size of %d bytes", item.Name, item.Size), "Retry with --images laptop or check the release manifest.")
	}
	if n != item.Size {
		return api.RuntimeInstalledFile{}, UserError("digest_mismatch", fmt.Sprintf("%s download was cut short (%d of %d bytes)", item.Name, n, item.Size), "Retry the download or pass --images laptop.")
	}
	gotHex := hex.EncodeToString(h.Sum(nil))
	if gotHex != item.SHA256 {
		return api.RuntimeInstalledFile{}, UserError("digest_mismatch", fmt.Sprintf("%s does not match its manifest SHA-256 (got %s, want %s)", item.Name, gotHex[:12], item.SHA256[:12]), "Retry with --images laptop or run host upgrade.")
	}

	if err := placeVerifiedArtefact(partPath, dst, item.SHA256, item.Size, mode); err != nil {
		return api.RuntimeInstalledFile{}, err
	}
	cleanup = false
	return api.RuntimeInstalledFile{
		Kind:       item.Kind,
		Name:       item.Name,
		SHA256:     item.SHA256,
		Size:       item.Size,
		Path:       dst,
		DurationMS: time.Since(start).Milliseconds(),
	}, nil
}

func installOneRuntimePart(item api.RuntimeAsset, partName string) (api.RuntimeInstalledFile, error) {
	if partName == "" || filepath.Base(partName) != partName || !strings.HasSuffix(partName, ".part") ||
		strings.ContainsAny(partName, `/\`) || partName == "." || partName == ".." {
		return api.RuntimeInstalledFile{}, UserError("bad_request", fmt.Sprintf("invalid incoming part name %q", partName), "Update the client.")
	}

	incomingDir := paths.ArtefactsIncomingDir()
	start := time.Now()
	root, err := os.OpenRoot(incomingDir)
	if err != nil {
		return api.RuntimeInstalledFile{}, fmt.Errorf("open incoming directory: %w", err)
	}
	defer func() { _ = root.Close() }()

	cleanup := true
	defer func() {
		if cleanup {
			_ = root.Remove(partName)
		}
	}()

	fi, err := root.Lstat(partName)
	if err != nil {
		return api.RuntimeInstalledFile{}, UserError("runtime_missing", fmt.Sprintf("uploaded runtime part %s was not found", partName), "Retry the transfer.")
	}
	if !fi.Mode().IsRegular() {
		return api.RuntimeInstalledFile{}, UserError("bad_request", fmt.Sprintf("uploaded runtime part %s is not a regular file", partName), "Retry the transfer.")
	}
	if fi.Size() > item.Size {
		return api.RuntimeInstalledFile{}, UserError("runtime_too_big", fmt.Sprintf("%s is larger than its manifest size (%d > %d bytes)", item.Name, fi.Size(), item.Size), "Retry the transfer.")
	}

	f, err := root.Open(partName)
	if err != nil {
		return api.RuntimeInstalledFile{}, fmt.Errorf("open uploaded part %s: %w", partName, err)
	}
	h := sha256.New()
	n, copyErr := io.Copy(h, io.LimitReader(f, item.Size+1))
	_ = f.Close()
	if copyErr != nil {
		return api.RuntimeInstalledFile{}, fmt.Errorf("hash uploaded part %s: %w", partName, copyErr)
	}
	if n > item.Size {
		return api.RuntimeInstalledFile{}, UserError("runtime_too_big", fmt.Sprintf("%s exceeded its manifest size of %d bytes", item.Name, item.Size), "Retry the transfer.")
	}
	if n != item.Size {
		return api.RuntimeInstalledFile{}, UserError("digest_mismatch", fmt.Sprintf("%s upload was incomplete (%d of %d bytes)", item.Name, n, item.Size), "Retry the transfer.")
	}
	gotHex := hex.EncodeToString(h.Sum(nil))
	if gotHex != item.SHA256 {
		return api.RuntimeInstalledFile{}, UserError("digest_mismatch", fmt.Sprintf("%s does not match its manifest SHA-256 (got %s, want %s)", item.Name, gotHex[:12], item.SHA256[:12]), "Retry the transfer.")
	}

	partPath := filepath.Join(incomingDir, partName)
	dst := paths.ArtefactFile(item.SHA256)
	if err := placeVerifiedArtefact(partPath, dst, item.SHA256, item.Size, runtimeFileMode(item)); err != nil {
		return api.RuntimeInstalledFile{}, err
	}
	cleanup = false
	return api.RuntimeInstalledFile{
		Kind:       item.Kind,
		Name:       item.Name,
		SHA256:     item.SHA256,
		Size:       item.Size,
		Path:       dst,
		DurationMS: time.Since(start).Milliseconds(),
	}, nil
}

// placeVerifiedArtefact sets mode on partPath and moves it into dst. Per spec
// 14.9.3, when a valid file is already at dst, the existing file wins and the
// part file is removed; if dst is missing or corrupt, partPath replaces it
// atomically.
func placeVerifiedArtefact(partPath, dst, wantHex string, wantSize int64, mode os.FileMode) error {
	if err := os.Chmod(partPath, mode); err != nil {
		return fmt.Errorf("chmod runtime file: %w", err)
	}
	if err := os.Link(partPath, dst); err == nil {
		_ = os.Remove(partPath)
		return nil
	} else if errors.Is(err, os.ErrExist) {
		if verifyExistingArtefact(dst, wantHex, wantSize) {
			_ = os.Chmod(dst, mode)
			_ = os.Remove(partPath)
			return nil
		}
	}
	if err := os.Rename(partPath, dst); err != nil {
		return fmt.Errorf("install runtime artefact %s: %w", dst, err)
	}
	return nil
}

func verifyExistingArtefact(path, wantHex string, wantSize int64) bool {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return false
	}
	defer func() { _ = root.Close() }()
	name := filepath.Base(path)
	fi, err := root.Lstat(name)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() != wantSize {
		return false
	}
	f, err := root.Open(name)
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

func runtimeFileMode(item api.RuntimeAsset) os.FileMode {
	if item.Executable() {
		return 0o555
	}
	return 0o444
}

func randomHex(nBytes int) string {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
