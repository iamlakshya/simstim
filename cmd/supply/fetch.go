package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/supply"
)

const maxSupplyDownloadBytes = 256 << 20 // 256 MiB cap for archives and source tarballs

var supplyHTTPClient = func() *http.Client {
	return &http.Client{
		Timeout: 10 * time.Minute,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if !strings.EqualFold(req.URL.Scheme, "https") {
				return fmt.Errorf("refusing non-HTTPS redirect to %s", req.URL)
			}
			if len(via) >= 10 {
				return errors.New("too many redirects")
			}
			return nil
		},
	}
}

func runFetchKernel(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("fetch-kernel", flag.ContinueOnError)
	lockPath := fs.String("lock", "guest/supply.lock", "path to guest/supply.lock")
	archFlag := fs.String("arch", "all", "architecture: amd64, arm64 or all")
	outDir := fs.String("out", "build/guest", "output directory")
	checkScript := fs.String("check-config", "guest/kernel/check-config.sh", "path to check-config.sh (empty to skip)")
	mirrorSource := fs.Bool("mirror-source", false, "also download and verify linux-<version>.tar.xz")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("supply: parse fetch-kernel flags: %w", err)
	}
	lock, err := supply.LoadLock(*lockPath)
	if err != nil {
		return err
	}
	arches, err := parseArches(*archFlag)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*outDir, 0o700); err != nil {
		return fmt.Errorf("supply: create %s: %w", *outDir, err)
	}

	hc := supplyHTTPClient()
	for _, arch := range arches {
		if err := fetchOneKernel(ctx, hc, lock.Kernel, arch, *outDir, *checkScript); err != nil {
			return err
		}
	}
	if *mirrorSource {
		dst := filepath.Join(*outDir, supply.KernelSourceAssetName(lock.Kernel.Version))
		if err := ensureVerifiedAsset(ctx, hc, lock.Kernel.SourceTarball, dst, 0o444); err != nil {
			return err
		}
		fmt.Printf("supply: verified %s (%d bytes)\n", dst, lock.Kernel.SourceTarball.Size)
	}
	return nil
}

func fetchOneKernel(ctx context.Context, hc *http.Client, k supply.KernelSpec, arch, outDir, checkScript string) error {
	ka, err := k.Arch(arch)
	if err != nil {
		return err
	}
	kernelName, err := supply.KernelAssetName(arch)
	if err != nil {
		return err
	}
	kernelDst := filepath.Join(outDir, kernelName)
	configDst := filepath.Join(outDir, supply.ConfigAssetName(arch))

	if !assetPresentAndValid(kernelDst, ka.SHA256, ka.Size) || !assetPresentAndValid(configDst, ka.ConfigSHA256, ka.ConfigSize) {
		archivePin := supply.AssetPin{URL: ka.URL, SHA256: ka.ArchiveSHA256, Size: ka.ArchiveSize}
		archiveTmp, err := downloadVerifiedToTemp(ctx, hc, outDir, archivePin)
		if err != nil {
			return err
		}
		defer os.Remove(archiveTmp)

		extractDir, err := os.MkdirTemp(outDir, ".kata-extract-*")
		if err != nil {
			return fmt.Errorf("supply: create extract directory: %w", err)
		}
		defer os.RemoveAll(extractDir)

		out, err := exec.CommandContext(ctx, "tar", "--zstd", "-xf", archiveTmp, "-C", extractDir, k.ArchiveMember, k.ConfigMember).CombinedOutput()
		if err != nil {
			return fmt.Errorf("supply: extract Kata kernel (%s): %w. Try: ensure tar and zstd are installed", strings.TrimSpace(string(out)), err)
		}
		if err := installExtractedPin(filepath.Join(extractDir, filepath.FromSlash(strings.TrimPrefix(k.ArchiveMember, "./"))), kernelDst, ka.SHA256, ka.Size); err != nil {
			return err
		}
		if err := installExtractedPin(filepath.Join(extractDir, filepath.FromSlash(strings.TrimPrefix(k.ConfigMember, "./"))), configDst, ka.ConfigSHA256, ka.ConfigSize); err != nil {
			return err
		}
	}

	if checkScript != "" {
		out, err := exec.CommandContext(ctx, "bash", checkScript, configDst, arch).CombinedOutput()
		if err != nil {
			return fmt.Errorf("supply: check-config failed for %s:\n%s", arch, strings.TrimSpace(string(out)))
		}
	}
	fmt.Printf("supply: kernel %s ok (%s, %d bytes; %s checked)\n", arch, kernelName, ka.Size, supply.ConfigAssetName(arch))
	return nil
}

func runMirrorVMM(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("mirror-vmm", flag.ContinueOnError)
	lockPath := fs.String("lock", "guest/supply.lock", "path to guest/supply.lock")
	archFlag := fs.String("arch", "all", "architecture: amd64, arm64 or all")
	outDir := fs.String("out", "build/guest", "output directory")
	withSource := fs.Bool("source", false, "also mirror each VMM's pinned source archive")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("supply: parse mirror-vmm flags: %w", err)
	}
	lock, err := supply.LoadLock(*lockPath)
	if err != nil {
		return err
	}
	arches, err := parseArches(*archFlag)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*outDir, 0o700); err != nil {
		return fmt.Errorf("supply: create %s: %w", *outDir, err)
	}

	hc := supplyHTTPClient()
	for _, v := range lock.VMMs {
		for _, arch := range arches {
			binPin, remPin, err := v.BinaryFor(arch)
			if err != nil {
				return err
			}
			vmmDst := filepath.Join(*outDir, supply.VMMAssetName(v.Name, v.Version, arch))
			if err := ensureVerifiedAsset(ctx, hc, binPin, vmmDst, 0o555); err != nil {
				return err
			}
			remDst := filepath.Join(*outDir, supply.VMMRemoteAssetName(v.Version, arch))
			if err := ensureVerifiedAsset(ctx, hc, remPin, remDst, 0o555); err != nil {
				return err
			}
			fmt.Printf("supply: mirrored %s %s (%s)\n", v.Name, v.Version, arch)
		}
		if *withSource {
			srcDst := filepath.Join(*outDir, filepath.Base(v.Source.URL))
			if err := ensureVerifiedAsset(ctx, hc, v.Source, srcDst, 0o444); err != nil {
				return err
			}
			fmt.Printf("supply: mirrored %s source (%s)\n", v.Name, filepath.Base(srcDst))
		}
	}
	return nil
}

func assetPresentAndValid(path, wantSHA string, wantSize int64) bool {
	gotSHA, gotSize, err := supply.FileDigest(path)
	return err == nil && gotSize == wantSize && strings.EqualFold(gotSHA, wantSHA)
}

func installExtractedPin(src, dst, wantSHA string, wantSize int64) error {
	if !assetPresentAndValid(src, wantSHA, wantSize) {
		return fmt.Errorf("supply: %s does not match its SHA-256 or size in guest/supply.lock. Try: download it again. If it still differs, stop, and check upstream checksums", filepath.Base(dst))
	}
	//nolint:gosec // G302: read-only boot-pair asset (0444)
	if err := os.Chmod(src, 0o444); err != nil {
		return fmt.Errorf("supply: chmod %s: %w", dst, err)
	}
	if err := os.Rename(src, dst); err != nil {
		return fmt.Errorf("supply: install %s: %w", dst, err)
	}
	return nil
}

func ensureVerifiedAsset(ctx context.Context, hc *http.Client, pin supply.AssetPin, dst string, mode os.FileMode) error {
	if assetPresentAndValid(dst, pin.SHA256, pin.Size) {
		return nil
	}
	tmp, err := downloadVerifiedToTemp(ctx, hc, filepath.Dir(dst), pin)
	if err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("supply: chmod %s: %w", dst, err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("supply: install %s: %w", dst, err)
	}
	return nil
}

func downloadVerifiedToTemp(ctx context.Context, hc *http.Client, dir string, pin supply.AssetPin) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pin.URL, nil)
	if err != nil {
		return "", fmt.Errorf("supply: build request for %s: %w", pin.URL, err)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("supply: download %s: %w. Try: check network access and retry", pin.URL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("supply: download %s returned HTTP %d. Try: check guest/supply.lock", pin.URL, resp.StatusCode)
	}
	if pin.Size > 0 && resp.ContentLength > pin.Size {
		return "", fmt.Errorf("supply: %s is larger than its pinned size (%d > %d bytes). Try: check guest/supply.lock", filepath.Base(pin.URL), resp.ContentLength, pin.Size)
	}
	limit := pin.Size
	if limit <= 0 || limit > maxSupplyDownloadBytes {
		limit = maxSupplyDownloadBytes
	}
	tmp, err := os.CreateTemp(dir, ".supply-dl-*")
	if err != nil {
		return "", fmt.Errorf("supply: create temp download file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(resp.Body, limit+1))
	closeErr := tmp.Close()
	if copyErr != nil {
		return "", fmt.Errorf("supply: download %s stopped: %w. Try: retry the download", filepath.Base(pin.URL), copyErr)
	}
	if closeErr != nil {
		return "", fmt.Errorf("supply: close %s: %w", tmpPath, closeErr)
	}
	if pin.Size > 0 && n != pin.Size {
		return "", fmt.Errorf("supply: %s size is %d bytes, want %d bytes from guest/supply.lock. Try: download it again", filepath.Base(pin.URL), n, pin.Size)
	}
	gotHex := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(gotHex, pin.SHA256) {
		return "", fmt.Errorf("supply: %s does not match its SHA-256 in guest/supply.lock. Try: download it again. If it still differs, stop, and check upstream checksums", filepath.Base(pin.URL))
	}
	cleanup = false
	return tmpPath, nil
}
