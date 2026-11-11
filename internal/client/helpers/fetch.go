package helpers

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ReleaseSigners are the keys that sign releases' checksums.txt, as
// allowed-signers lines for ssh-keygen, one line for each key. A signature by
// any of them passes. During a key rotation the new key is listed beside the
// old one, until a release that the new key signed ships
// (docs/contributing/releasing.md). It is the same list as
// .github/release-signers and install.sh's; a test keeps them equal.
const ReleaseSigners = `release namespaces="file" ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGyv3BRbYeIpUvqwia5q4CuqngP+aUzouWZ8x73U2LOy`

// fetched holds helpers downloaded from a release, for a client built
// without them (go install).
var (
	fetchMu sync.Mutex
	fetched map[string][]byte
)

// Embedded is true when this build carries its helpers.
func Embedded() bool {
	names, _ := fsGlob("host-*")
	return len(names) > 0
}

// Fetch makes sure the helpers are available. A build that embeds them needs
// nothing. Otherwise it loads them from the cache, or downloads the release
// bundle for version from base (a GitHub releases URL, such as
// https://github.com/<owner>/<repo>/releases/download/v<version>), checks
// its signature with ssh-keygen and its SHA-256, and caches it in cacheDir.
func Fetch(ctx context.Context, version, base, cacheDir string) error {
	if Embedded() {
		return nil
	}
	fetchMu.Lock()
	defer fetchMu.Unlock()
	if fetched != nil {
		return nil
	}
	dir := filepath.Join(cacheDir, "helpers", version)
	if files, err := readDir(dir); err == nil && len(files) > 0 {
		fetched = files
		return nil
	}
	files, err := download(ctx, version, base)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("cache helpers: %w", err)
	}
	for name, b := range files {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
			return fmt.Errorf("cache helpers: %w", err)
		}
	}
	fetched = files
	return nil
}

func readDir(dir string) (map[string][]byte, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	files := map[string][]byte{}
	for _, e := range entries {
		if !e.Type().IsRegular() || !isHelper(e.Name()) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", e.Name(), err)
		}
		files[e.Name()] = b
	}
	return files, nil
}

// BundleName is the release asset that holds the helpers for version.
func BundleName(version string) string { return "helpers_" + version + ".tar.gz" }

func download(ctx context.Context, version, base string) (map[string][]byte, error) {
	get := func(name string) ([]byte, error) { return httpGet(ctx, strings.TrimSuffix(base, "/")+"/"+name) }
	sums, err := get("checksums.txt")
	if err != nil {
		return nil, err
	}
	sig, err := get("checksums.txt.sig")
	if err != nil {
		return nil, err
	}
	if err := verifySignature(ctx, sums, sig); err != nil {
		return nil, err
	}
	bundle, err := get(BundleName(version))
	if err != nil {
		return nil, err
	}
	want := checksumOf(sums, BundleName(version))
	got := sha256.Sum256(bundle)
	if want == "" || want != hex.EncodeToString(got[:]) {
		return nil, errors.New("the helper bundle's checksum does not match the signed checksums.txt")
	}
	return untar(bundle)
}

func httpGet(ctx context.Context, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", url, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: %s", url, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", url, err)
	}
	return b, nil
}

// trusted holds the allowed-signers lines; tests replace it.
var trusted = ReleaseSigners

// verifySignature checks checksums.txt against its SSH signature with
// ssh-keygen, which every machine with OpenSSH has.
func verifySignature(ctx context.Context, sums, sig []byte) error {
	dir, err := os.MkdirTemp("", "verify")
	if err != nil {
		return fmt.Errorf("verify the release: %w", err)
	}
	defer os.RemoveAll(dir)
	signers, sigFile := filepath.Join(dir, "signers"), filepath.Join(dir, "sig")
	if err := os.WriteFile(signers, []byte(trusted+"\n"), 0o600); err != nil {
		return fmt.Errorf("verify the release: %w", err)
	}
	if err := os.WriteFile(sigFile, sig, 0o600); err != nil {
		return fmt.Errorf("verify the release: %w", err)
	}
	cmd := exec.CommandContext(ctx, "ssh-keygen", "-Y", "verify", "-f", signers, "-I", "release", "-n", "file", "-s", sigFile)
	cmd.Stdin = bytes.NewReader(sums)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("the release's signature does not match the release key: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

func checksumOf(sums []byte, name string) string {
	for _, line := range strings.Split(string(sums), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			return f[0]
		}
	}
	return ""
}

func untar(bundle []byte) (map[string][]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(bundle))
	if err != nil {
		return nil, fmt.Errorf("open the helper bundle: %w", err)
	}
	tr := tar.NewReader(zr)
	files := map[string][]byte{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read the helper bundle: %w", err)
		}
		name := filepath.Base(h.Name)
		if h.Typeflag != tar.TypeReg || !isHelper(name) {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(tr, 64<<20))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		files[name] = b
	}
	if len(files) == 0 {
		return nil, errors.New("the helper bundle holds no helpers")
	}
	return files, nil
}

// isHelper is true for a host helper's or a shadow terminal's file name.
func isHelper(name string) bool {
	return strings.HasPrefix(name, "host-") || strings.HasPrefix(name, "vt-")
}
