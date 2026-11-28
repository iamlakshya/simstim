// Package gvisor implements the W2 gVisor wall on Linux using pinned upstream
// runsc releases with no patches (isolation spec 21.4, decision 0053).
package gvisor

import (
	"archive/tar"
	"compress/bzip2"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/paths"
)

const (
	// PinnedRelease is the pinned upstream gVisor release tag (guest/supply.lock).
	PinnedRelease = "release-20261123.1"
	// PinnedVersion is the upstream storage path release identifier.
	PinnedVersion = "20261123.1"
	// PinnedCommit is the git commit of PinnedRelease in google/gvisor.
	PinnedCommit = "81f68cb7d4661a9313d891a1758a765abe8eb27e"

	// RuntimeArtifactName is the directory and receipt stem under artefacts/runtime/.
	RuntimeArtifactName = "gvisor-" + PinnedRelease

	maxBinaryBytes  = 128 << 20 // 128 MiB per extracted binary
	maxTarballBytes = 256 << 20 // 256 MiB for compressed release archive
)

// RuntimePin describes the pinned upstream gVisor tarball and required runtime
// binaries for one CPU architecture.
type RuntimePin struct {
	Arch          string
	UpstreamArch  string
	Release       string
	TarballURL    string
	TarballSHA256 string
	Binaries      map[string]string // clean tar path -> lowercase SHA-256 hex
}

// requiredBinaries are the upstream runsc binary and gvisor-bin sidecars that
// release-20261123.1 executes when starting a sandbox.
var requiredBinaries = []string{
	"runsc",
	"gvisor-bin/gvisor_sentry",
	"gvisor-bin/gvisor-sentry-prewarmer",
	"gvisor-bin/gvisor-proc-umounter",
	"gvisor-bin/runsc-fd-parking",
}

var runtimePins = map[string]RuntimePin{
	"amd64": {
		Arch:          "amd64",
		UpstreamArch:  "x86_64",
		Release:       PinnedRelease,
		TarballURL:    "https://storage.googleapis.com/gvisor/releases/release/" + PinnedVersion + "/x86_64/gvisor.tar.bz2",
		TarballSHA256: "95c23cd8eda66eab351717e5ee2b1a5703f848bb73ac855a7b281e1c6cd178e8",
		Binaries: map[string]string{
			"runsc":                              "540c5824b3df0786a7a12ce06c9c9b0e05447a4543bcd859dd6b34bf67b218fb",
			"gvisor-bin/gvisor_sentry":           "572209d25162a1643bc7f1212201dccead4bbd1a8e59b69ba227c8afa5353df4",
			"gvisor-bin/gvisor-sentry-prewarmer": "f042cd65806680fd109d18d588c52a97c4f56eb4270d3b2033dce74e6049e672",
			"gvisor-bin/gvisor-proc-umounter":    "b3bbbaef5ddc62084e5febd19d3acca14f3d42b2a25d8552fb40def699467019",
			"gvisor-bin/runsc-fd-parking":        "03af90d07ea466c10ecc3e3ba839b2a59540959dd3286aea3886917cba850c11",
		},
	},
	"arm64": {
		Arch:          "arm64",
		UpstreamArch:  "aarch64",
		Release:       PinnedRelease,
		TarballURL:    "https://storage.googleapis.com/gvisor/releases/release/" + PinnedVersion + "/aarch64/gvisor.tar.bz2",
		TarballSHA256: "0088b9c58959f58118c27375a1941db31fd9f3c62e484c4ed2094f25dda693bf",
		Binaries: map[string]string{
			"runsc":                              "67efdf157c23e8410dc68601b7dcc048f2a30bf123dacb00e107228a174e076f",
			"gvisor-bin/gvisor_sentry":           "2206f534b6b567602c9d542ab654e44e6ef0138bb14dd6931361d4550af560aa",
			"gvisor-bin/gvisor-sentry-prewarmer": "1bbe2fa50f27c85a4c295cadd868e4fcbbe2aebb8c5b3aa0e57f324ae9d23557",
			"gvisor-bin/gvisor-proc-umounter":    "8ddecb8894b332be68e84faa073c073bbf1d6f046974d411fc02db03a73fde90",
			"gvisor-bin/runsc-fd-parking":        "e41db2da554154f282f2829a664a2c4234c163b9da74fc9b4388126223c7090c",
		},
	},
}

// PinForArch returns the pinned upstream gVisor release metadata for arch.
func PinForArch(arch string) (RuntimePin, error) {
	pin, ok := runtimePins[arch]
	if !ok {
		return RuntimePin{}, fmt.Errorf("gvisor: unsupported architecture %q (need amd64 or arm64). Try: use an amd64 or arm64 Linux host", arch)
	}
	return pin, nil
}

// RuntimeDir returns the artefact store directory holding the pinned runsc
// binary and its gvisor-bin sidecars on this host.
func RuntimeDir() string {
	return filepath.Join(paths.ArtefactsDir(), "runtime", RuntimeArtifactName)
}

// FindPinnedRunsc checks that the pinned upstream runsc binary and all of its
// required gvisor-bin sidecars exist under RuntimeDir() with matching SHA-256
// digests, returning the absolute path to runsc.
func FindPinnedRunsc(arch string) (string, error) {
	return VerifyRuntimeDir(RuntimeDir(), arch)
}

// VerifyRuntimeDir checks every pinned binary under dir against arch's SHA-256
// digests and returns the path to runsc.
func VerifyRuntimeDir(dir, arch string) (string, error) {
	pin, err := PinForArch(arch)
	if err != nil {
		return "", err
	}
	for _, rel := range requiredBinaries {
		want := pin.Binaries[rel]
		p := filepath.Join(dir, filepath.FromSlash(rel))
		got, herr := fileSHA256(p)
		if herr != nil {
			if errors.Is(herr, os.ErrNotExist) {
				return "", fmt.Errorf("gvisor: pinned runsc %s (%s) is missing from %s: %w. Try: install the pinned gVisor runtime on this host", pin.Release, rel, dir, herr)
			}
			return "", fmt.Errorf("gvisor: read pinned binary %s: %w. Try: reinstall the pinned gVisor runtime", rel, herr)
		}
		if got != want {
			return "", fmt.Errorf("gvisor: SHA-256 mismatch for %s (got %s, want %s). Try: reinstall the pinned gVisor runtime", rel, got, want)
		}
	}
	return filepath.Join(dir, "runsc"), nil
}

// EnsureRuntime ensures the pinned upstream gVisor runtime for arch is present
// and verified in RuntimeDir(), downloading and verifying the official tarball
// from storage.googleapis.com if needed.
func EnsureRuntime(ctx context.Context, arch string) (string, error) {
	if arch == "" {
		arch = runtime.GOARCH
	}
	if p, err := FindPinnedRunsc(arch); err == nil {
		return p, nil
	}
	pin, err := PinForArch(arch)
	if err != nil {
		return "", err
	}
	incoming := paths.ArtefactsIncomingDir()
	if err := paths.EnsurePrivateDir(incoming); err != nil {
		return "", err
	}
	tmpFile, err := os.CreateTemp(incoming, "gvisor-*.tar.bz2")
	if err != nil {
		return "", fmt.Errorf("create download temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	if err := downloadHTTPS(ctx, pin.TarballURL, tmpFile); err != nil {
		_ = tmpFile.Close()
		return "", err
	}
	if err := tmpFile.Close(); err != nil {
		return "", fmt.Errorf("close downloaded archive: %w", err)
	}
	if err := InstallFromTarball(tmpPath, RuntimeDir(), arch); err != nil {
		return "", err
	}
	if err := writeRuntimeReceipt(filepath.Dir(RuntimeDir()), pin); err != nil {
		return "", err
	}
	return filepath.Join(RuntimeDir(), "runsc"), nil
}

// InstallFromTarball verifies tarballPath against arch's pinned SHA-256 digest,
// extracts only the allowlisted runsc and gvisor-bin binaries into destDir, and
// verifies every extracted binary's SHA-256 digest.
func InstallFromTarball(tarballPath, destDir, arch string) error {
	pin, err := PinForArch(arch)
	if err != nil {
		return err
	}
	gotTar, err := fileSHA256(tarballPath)
	if err != nil {
		return fmt.Errorf("hash gVisor archive: %w", err)
	}
	if gotTar != pin.TarballSHA256 {
		return fmt.Errorf("gvisor: archive SHA-256 mismatch (got %s, want %s). Try: retry the download", gotTar, pin.TarballSHA256)
	}

	parent := filepath.Dir(destDir)
	if err := paths.EnsurePrivateDir(parent); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(parent, ".gvisor-stage-*")
	if err != nil {
		return fmt.Errorf("create gVisor staging dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(stage) }()

	if err := extractAllowlistedTarBZ2(tarballPath, stage, pin.Binaries); err != nil {
		return err
	}
	if _, err := VerifyRuntimeDir(stage, arch); err != nil {
		return err
	}
	_ = os.RemoveAll(destDir)
	if err := os.Rename(stage, destDir); err != nil {
		return fmt.Errorf("install gVisor runtime at %s: %w", destDir, err)
	}
	return nil
}

func extractAllowlistedTarBZ2(tarballPath, stageDir string, want map[string]string) error {
	f, err := os.Open(tarballPath)
	if err != nil {
		return fmt.Errorf("open gVisor archive: %w", err)
	}
	defer f.Close()

	tr := tar.NewReader(bzip2.NewReader(f))
	extracted := map[string]bool{}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read gVisor archive: %w", err)
		}
		clean := strings.TrimPrefix(filepath.ToSlash(filepath.Clean(hdr.Name)), "./")
		wantDigest, ok := want[clean]
		if !ok {
			continue
		}
		if hdr.Typeflag != tar.TypeReg {
			return fmt.Errorf("gvisor: archive entry %s is not a regular file", clean)
		}
		if err := writeVerifiedEntry(stageDir, clean, tr, wantDigest); err != nil {
			return err
		}
		extracted[clean] = true
	}
	for _, rel := range requiredBinaries {
		if !extracted[rel] {
			return fmt.Errorf("gvisor: archive missing required binary %s", rel)
		}
	}
	return nil
}

func writeVerifiedEntry(stageDir, rel string, r io.Reader, wantDigest string) error {
	dest := filepath.Join(stageDir, filepath.FromSlash(rel))
	if err := paths.EnsurePrivateDir(filepath.Dir(dest)); err != nil {
		return err
	}
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o700) //nolint:gosec // G304: rel is from fixed allowlist inside stageDir
	if err != nil {
		return fmt.Errorf("create %s: %w", rel, err)
	}
	h := sha256.New()
	limited := io.LimitReader(r, maxBinaryBytes+1)
	n, copyErr := io.Copy(io.MultiWriter(out, h), limited)
	closeErr := out.Close()
	if copyErr != nil {
		return fmt.Errorf("extract %s: %w", rel, copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close %s: %w", rel, closeErr)
	}
	if n > maxBinaryBytes {
		return fmt.Errorf("gvisor: extracted binary %s exceeds size limit", rel)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != wantDigest {
		return fmt.Errorf("gvisor: SHA-256 mismatch for %s (got %s, want %s)", rel, got, wantDigest)
	}
	return nil
}

func downloadHTTPS(ctx context.Context, rawURL string, dst io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return fmt.Errorf("prepare gVisor download: %w", err)
	}
	cl := &http.Client{
		Timeout: 5 * time.Minute,
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if !strings.EqualFold(r.URL.Scheme, "https") {
				return fmt.Errorf("refusing non-HTTPS redirect to %s", r.URL.Scheme)
			}
			return nil
		},
	}
	resp, err := cl.Do(req)
	if err != nil {
		return fmt.Errorf("download gVisor runtime: %w. Try: check outbound HTTPS access to storage.googleapis.com", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download gVisor runtime: HTTP %d. Try: check outbound HTTPS access to storage.googleapis.com", resp.StatusCode)
	}
	n, err := io.Copy(dst, io.LimitReader(resp.Body, maxTarballBytes+1))
	if err != nil {
		return fmt.Errorf("save gVisor archive: %w", err)
	}
	if n > maxTarballBytes {
		return errors.New("gvisor: downloaded archive exceeds size limit")
	}
	return nil
}

func writeRuntimeReceipt(runtimeRoot string, pin RuntimePin) error {
	doc := struct {
		Kind          string            `json:"kind"`
		Name          string            `json:"name"`
		Release       string            `json:"release"`
		Commit        string            `json:"commit"`
		Arch          string            `json:"arch"`
		TarballSHA256 string            `json:"tarball_sha256"`
		Binaries      map[string]string `json:"binaries"`
	}{
		Kind:          "gvisor",
		Name:          "runsc",
		Release:       pin.Release,
		Commit:        PinnedCommit,
		Arch:          pin.Arch,
		TarballSHA256: pin.TarballSHA256,
		Binaries:      pin.Binaries,
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encode gVisor receipt: %w", err)
	}
	root, err := os.OpenRoot(runtimeRoot)
	if err != nil {
		return fmt.Errorf("open runtime dir: %w", err)
	}
	defer root.Close()
	if err := root.WriteFile(RuntimeArtifactName+".json", append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("write gVisor receipt: %w", err)
	}
	return nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("hash %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
