// Command supply manages pinned microvm boot-pair inputs, builds deterministic
// dm-verity tools images, writes and verifies release-manifest.json, and runs
// the Cloud Hypervisor guest self-test boot check (isolation spec 20.1).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/iamlakshya/simstim/internal/wall/chv"
)

const usageText = `Usage: go run ./cmd/supply <command> [flags]

Commands:
  check-lock       validate guest/supply.lock and pin consistency
  fetch-kernel     fetch and verify Kata 6.18 guest kernel(s) and config(s)
  build-tools      build a deterministic dm-verity EROFS tools image
  mirror-vmm       fetch and verify pinned VMM binaries and source archives
  manifest         build and verify release-manifest.json
  verify-manifest  check every asset in release-manifest.json on disk
  boot-test        boot kernel + tools under pinned Cloud Hypervisor (--self-test)
`

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usageText)
		return errors.New("supply: missing command. Try: go run ./cmd/supply check-lock")
	}
	switch args[0] {
	case "check-lock":
		return runCheckLock(args[1:])
	case "fetch-kernel":
		return runFetchKernel(ctx, args[1:])
	case "build-tools":
		return runBuildTools(ctx, args[1:])
	case "mirror-vmm":
		return runMirrorVMM(ctx, args[1:])
	case "manifest":
		return runManifest(args[1:])
	case "verify-manifest":
		return runVerifyManifest(args[1:])
	case "boot-test":
		return runBootTest(ctx, args[1:])
	case "-h", "--help", "help":
		fmt.Print(usageText)
		return nil
	default:
		return fmt.Errorf("supply: unknown command %q. Try: go run ./cmd/supply --help", args[0])
	}
}

func runCheckLock(args []string) error {
	fs := flag.NewFlagSet("check-lock", flag.ContinueOnError)
	lockPath := fs.String("lock", "guest/supply.lock", "path to guest/supply.lock")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("supply: parse flags: %w", err)
	}
	lock, err := supply.LoadLock(*lockPath)
	if err != nil {
		return err
	}
	fmt.Printf("supply: %s ok (kernel %s via kata %s, %d VMMs, %d pins)\n",
		*lockPath, lock.Kernel.Version, lock.Kernel.KataVersion, len(lock.VMMs), len(lock.Pins))
	return nil
}

func runBuildTools(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("build-tools", flag.ContinueOnError)
	arch := fs.String("arch", "amd64", "target architecture: amd64 or arm64")
	helper := fs.String("helper", "", "path to static Linux host helper binary")
	vt := fs.String("vt", "", "optional path to shadow terminal binary")
	outDir := fs.String("out", "build/guest", "output directory")
	epochStr := fs.String("epoch", os.Getenv("SOURCE_DATE_EPOCH"), "unix timestamp for deterministic EROFS metadata")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("supply: parse flags: %w", err)
	}
	if *arch != "amd64" && *arch != "arm64" {
		return fmt.Errorf("supply: invalid arch %q. Try: pass --arch amd64 or arm64", *arch)
	}
	if *helper == "" {
		return errors.New("supply: --helper is required. Try: build host helpers with `just helpers` first")
	}
	var epoch int64
	if strings.TrimSpace(*epochStr) != "" {
		n, err := strconv.ParseInt(strings.TrimSpace(*epochStr), 10, 64)
		if err != nil || n < 0 {
			return fmt.Errorf("supply: invalid --epoch %q. Try: pass a non-negative Unix timestamp", *epochStr)
		}
		epoch = n
	}

	outPath := filepath.Join(*outDir, supply.ToolsAssetName(*arch))
	opts := chv.ToolsBuildOptions{Epoch: epoch}
	if *vt != "" {
		opts.ShadowVT = *vt
		opts.VTName = brand.VTBinary
	}
	rec, err := chv.BuildToolsImage(ctx, *helper, outPath, opts)
	if err != nil {
		return err
	}
	meta := supply.VerityInfo{
		SHA256:     rec.SHA256,
		Size:       rec.Size,
		RootHash:   rec.RootHash,
		Salt:       rec.Salt,
		DataBlocks: rec.DataBlocks,
	}
	raw, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("supply: encode tools metadata: %w", err)
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(outPath+".json", raw, 0o600); err != nil {
		return fmt.Errorf("supply: write tools metadata: %w", err)
	}
	fmt.Printf("supply: built %s (%d bytes, root_hash=%s, data_blocks=%d)\n",
		outPath, rec.Size, rec.RootHash[:16], rec.DataBlocks)
	return nil
}

func runManifest(args []string) error {
	fs := flag.NewFlagSet("manifest", flag.ContinueOnError)
	lockPath := fs.String("lock", "guest/supply.lock", "path to guest/supply.lock")
	release := fs.String("release", "", "release version (e.g. 0.6.0)")
	runtimeLabel := fs.String("runtime", "", "runtime label (e.g. 2026.11.0)")
	baseURL := fs.String("base-url", "", "release download base URL")
	dir := fs.String("dir", "build/guest", "directory containing built boot-pair assets")
	outPath := fs.String("out", "", "output path for release-manifest.json (defaults to <dir>/release-manifest.json)")
	archesFlag := fs.String("arch", "amd64,arm64", "comma-separated architectures")
	requireVMM := fs.Bool("require-vmm", false, "fail if mirrored VMM binaries are missing")
	requireSource := fs.Bool("require-source", false, "fail if mirrored kernel source tarball is missing")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("supply: parse flags: %w", err)
	}
	lock, err := supply.LoadLock(*lockPath)
	if err != nil {
		return err
	}
	var arches []string
	for _, a := range strings.Split(*archesFlag, ",") {
		if a = strings.TrimSpace(a); a != "" {
			arches = append(arches, a)
		}
	}
	label := strings.TrimSpace(*runtimeLabel)
	if label == "" {
		label = chv.RuntimeLabel
	}
	out := *outPath
	if out == "" {
		out = filepath.Join(*dir, "release-manifest.json")
	}
	m, err := supply.BuildReleaseManifest(supply.BuildManifestOptions{
		Lock:          lock,
		Release:       *release,
		Runtime:       label,
		BaseURL:       *baseURL,
		AssetDir:      *dir,
		Arches:        arches,
		RequireVMM:    *requireVMM,
		RequireSource: *requireSource,
	})
	if err != nil {
		return err
	}
	if err := supply.WriteReleaseManifest(out, m); err != nil {
		return err
	}
	fmt.Printf("supply: wrote %s (%d items, runtime=%s)\n", out, len(m.Items), m.Runtime)
	return nil
}

func runVerifyManifest(args []string) error {
	fs := flag.NewFlagSet("verify-manifest", flag.ContinueOnError)
	manifestPath := fs.String("manifest", "build/guest/release-manifest.json", "path to release-manifest.json")
	dir := fs.String("dir", "build/guest", "directory containing built assets")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("supply: parse flags: %w", err)
	}
	m, err := supply.LoadReleaseManifest(*manifestPath)
	if err != nil {
		return err
	}
	if err := supply.VerifyReleaseManifest(m, *dir); err != nil {
		return err
	}
	fmt.Printf("supply: verified %s (%d items ok)\n", *manifestPath, len(m.Items))
	return nil
}

func parseArches(raw string) ([]string, error) {
	if raw == "all" || raw == "" {
		return []string{"amd64", "arm64"}, nil
	}
	var out []string
	for _, a := range strings.Split(raw, ",") {
		a = strings.TrimSpace(a)
		if a != "amd64" && a != "arm64" {
			return nil, fmt.Errorf("supply: unsupported architecture %q. Try: pass amd64, arm64 or all", a)
		}
		out = append(out, a)
	}
	return out, nil
}
