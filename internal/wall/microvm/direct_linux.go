//go:build linux

package microvm

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/iamlakshya/simstim/internal/wall"
)

// DirectResult is a host-owned record of the controlled guest512-byte direct
// write test. A guest's ordinary status claim is not this proof. Its recorder
// is called only by the admitted fixed test flow, never by a guest operation.
type DirectResult struct {
	Version      int       `json:"version"`
	Test         string    `json:"test"`
	VMMSHA256    string    `json:"vmm_sha256"`
	KernelSHA256 string    `json:"kernel_sha256"`
	DiskBytes    int64     `json:"disk_bytes"`
	Passed       bool      `json:"passed"`
	CheckedAt    time.Time `json:"checked_at"`
}

func sessionDirect(spec wall.Spec) (bool, error) {
	root, err := cachefs.OpenPrivateRoot(spec.SessionDir, false)
	if err != nil {
		return false, err
	}
	defer root.Close()
	file, err := cachefs.OpenFile(root, "direct-io.json", os.O_RDONLY, 0o600)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil {
		return false, fmt.Errorf("read guest direct-I/O proof: %w", err)
	}
	if len(data) > 4096 {
		return false, fmt.Errorf("guest direct-I/O proof exceeds its bound")
	}
	var result DirectResult
	if err := json.Unmarshal(data, &result); err != nil {
		return false, fmt.Errorf("decode guest direct-I/O proof: %w", err)
	}
	vmm, _ := spec.VM.Runtime.Find(supply.KindVMM, "cloud-hypervisor", spec.VM.Image.Arch)
	kernel, _ := spec.VM.Runtime.Find(supply.KindKernel, "guest", spec.VM.Image.Arch)
	if result.Version != 1 || result.Test != "guest-dd-512-direct" || result.CheckedAt.IsZero() {
		return false, fmt.Errorf("direct-I/O proof is not a controlled guest test")
	}
	if result.VMMSHA256 != vmm.SHA256 || result.KernelSHA256 != kernel.SHA256 || result.DiskBytes != spec.DiskMax {
		return true, nil
	}
	return result.Passed, nil
}

// RecordDirectResult records a completed admitted controlled guest check. The
// caller supplies its canonical expected plan; neither guest claims nor a
// mutable host runtime inventory can choose the recorded byte identities.
func RecordDirectResult(ctx context.Context, spec wall.Spec, passed bool) error {
	if err := validateVMSpec(spec); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	vmm, ok := spec.VM.Runtime.Find(supply.KindVMM, "cloud-hypervisor", spec.VM.Image.Arch)
	if !ok {
		return fmt.Errorf("guest direct-I/O proof has no client VMM pin")
	}
	kernel, ok := spec.VM.Runtime.Find(supply.KindKernel, "guest", spec.VM.Image.Arch)
	if !ok {
		return fmt.Errorf("guest direct-I/O proof has no client kernel pin")
	}
	root, err := cachefs.OpenPrivateRoot(spec.SessionDir, false)
	if err != nil {
		return err
	}
	defer root.Close()
	result := DirectResult{Version: 1, Test: "guest-dd-512-direct", VMMSHA256: vmm.SHA256, KernelSHA256: kernel.SHA256, DiskBytes: spec.DiskMax, Passed: passed, CheckedAt: time.Now().UTC()}
	data, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("encode guest direct-I/O proof: %w", err)
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return fmt.Errorf("name guest direct-I/O proof: %w", err)
	}
	name := ".direct-io-" + hex.EncodeToString(nonce[:])
	file, err := cachefs.OpenFile(root, name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer root.Remove(name)
	_, writeErr := file.Write(append(data, '\n'))
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return fmt.Errorf("write guest direct-I/O proof: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := root.Rename(name, "direct-io.json"); err != nil {
		return fmt.Errorf("publish guest direct-I/O proof: %w", err)
	}
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	return cachefs.SyncDirectory(dir)
}
