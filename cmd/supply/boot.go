package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/iamlakshya/simstim/internal/wall/chv"
)

func runBootTest(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("boot-test", flag.ContinueOnError)
	arch := fs.String("arch", runtime.GOARCH, "architecture to boot: amd64 or arm64")
	dir := fs.String("dir", "build/guest", "directory containing the built boot pair and mirrored VMM")
	lockPath := fs.String("lock", "guest/supply.lock", "path to guest/supply.lock")
	timeout := fs.Duration("timeout", 60*time.Second, "boot self-test timeout")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("boot test: parse flags: %w", err)
	}
	if *arch != runtime.GOARCH {
		return fmt.Errorf("boot test: cannot boot %s guest on %s host. Try: run `just guest-boot %s` on an %s Linux host with /dev/kvm", *arch, runtime.GOARCH, *arch, *arch)
	}
	absDir, err := filepath.Abs(*dir)
	if err != nil {
		return fmt.Errorf("boot test: resolve %s: %w. Try: pass an existing directory to --dir", *dir, err)
	}
	cfg, err := prepareBootConfig(*lockPath, absDir, *arch)
	if err != nil {
		return err
	}

	sockDir, err := os.MkdirTemp("/tmp", "chv-boot-*")
	if err != nil {
		return fmt.Errorf("boot test: create socket dir: %w", err)
	}
	defer os.RemoveAll(sockDir)

	cfg.APISocket = filepath.Join(sockDir, "self.vmm")
	cfg.VsockSocket = filepath.Join(sockDir, "self.vsock")
	sb := chv.NewSandbox(chv.SandboxOptions{Config: cfg, SkipVsock: true})
	if _, err := sb.Start(ctx); err != nil {
		return fmt.Errorf("boot test: start %s guest: %w", *arch, err)
	}
	defer func() { _ = sb.Destroy() }()

	done := make(chan struct{})
	go func() {
		sb.Wait()
		close(done)
	}()

	timer := time.NewTimer(*timeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-ctx.Done():
		_ = sb.Kill()
		<-done
		return fmt.Errorf("boot test: %s guest cancelled: %w", *arch, ctx.Err())
	case <-timer.C:
		_ = sb.Kill()
		<-done
		fmt.Fprintf(os.Stderr, "%s\n", sb.Stderr())
		return fmt.Errorf("boot test: the %s guest did not answer within %d s. Try: `just guest-boot %s`, which prints the guest's serial console",
			*arch, int(timeout.Seconds()), *arch)
	}
	serial := sb.Stderr()
	fmt.Fprint(os.Stderr, serial)
	if !strings.Contains(serial, "self-test ok") {
		return fmt.Errorf("boot test: the %s guest exited without printing self-test ok. Try: inspect the serial console output above", *arch)
	}
	fmt.Printf("boot test: %s self-test ok\n", *arch)
	return nil
}

func prepareBootConfig(lockPath, absDir, arch string) (chv.VMConfig, error) {
	lock, err := supply.LoadLock(lockPath)
	if err != nil {
		return chv.VMConfig{}, err
	}
	ka, err := lock.Kernel.Arch(arch)
	if err != nil {
		return chv.VMConfig{}, err
	}
	kernelName, err := supply.KernelAssetName(arch)
	if err != nil {
		return chv.VMConfig{}, err
	}
	kernelPath := filepath.Join(absDir, kernelName)
	if err := verifyPinnedFile(kernelPath, ka.SHA256, ka.Size, "kernel", arch); err != nil {
		return chv.VMConfig{}, err
	}

	toolsPath := filepath.Join(absDir, supply.ToolsAssetName(arch))
	metaRaw, err := os.ReadFile(filepath.Clean(toolsPath + ".json"))
	if err != nil {
		return chv.VMConfig{}, fmt.Errorf("boot test: read %s.json: %w. Try: run `just guest %s` first", toolsPath, err, arch)
	}
	var meta supply.VerityInfo
	if err := json.Unmarshal(metaRaw, &meta); err != nil {
		return chv.VMConfig{}, fmt.Errorf("boot test: decode %s.json: %w", toolsPath, err)
	}
	if _, err := supply.VerifyRawVerity(toolsPath, meta.DataBlocks, meta.RootHash); err != nil {
		return chv.VMConfig{}, err
	}

	vmm, err := lock.FindVMM("cloud-hypervisor")
	if err != nil {
		return chv.VMConfig{}, err
	}
	vmmPin, _, err := vmm.BinaryFor(arch)
	if err != nil {
		return chv.VMConfig{}, err
	}
	vmmPath := filepath.Join(absDir, supply.VMMAssetName(vmm.Name, vmm.Version, arch))
	if err := verifyPinnedFile(vmmPath, vmmPin.SHA256, vmmPin.Size, "cloud-hypervisor", arch); err != nil {
		return chv.VMConfig{}, err
	}
	remotePath := filepath.Join(absDir, supply.VMMRemoteAssetName(vmm.Version, arch))

	return chv.VMConfig{
		VMMPath:       vmmPath,
		CHRemotePath:  remotePath,
		KernelPath:    kernelPath,
		CPUs:          1,
		MemoryBytes:   512 << 20,
		SerialConsole: true,
		InitArgs:      []string{"wall-init", "--self-test"},
		Tools: chv.DiskVerity{
			Path:          toolsPath,
			RootHash:      meta.RootHash,
			DataBlocks:    meta.DataBlocks,
			HasSuperblock: false,
		},
	}, nil
}

func verifyPinnedFile(path, wantSHA string, wantSize int64, label, arch string) error {
	gotSHA, gotSize, err := supply.FileDigest(path)
	if err != nil {
		return fmt.Errorf("boot test: missing or unreadable %s (%s): %w. Try: run `just guest %s` first", label, path, err, arch)
	}
	if gotSize != wantSize || gotSHA != wantSHA {
		return fmt.Errorf("boot test: %s %s failed pin verification (size=%d/%d, sha256=%s/%s). Try: run `just guest %s` to refresh pinned artefacts",
			label, path, gotSize, wantSize, gotSHA, wantSHA, arch)
	}
	return nil
}
