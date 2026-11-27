//go:build linux

package microvm

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/iamlakshya/simstim/internal/supply"
)

type kernelLeaseCloseFailure struct {
	calls int
	err   error
}

func (c *kernelLeaseCloseFailure) Close() error { c.calls++; return c.err }

// This tests owned descriptor cleanup after the existing physical-lifetime
// owner calls Close. It does not simulate or claim a real VMM physical exit.
func TestKernelOwnerClosesDespiteAgentsLeaseErrorAndJoinsOnce(t *testing.T) {
	data := []byte("owned kernel descriptor cleanup fixture")
	base := supply.ReleaseFolder("1.2.3") + "/"
	item := supply.NewItem(supply.KindKernel, "guest", "6.18", runtime.GOARCH, data, base+supply.KernelAsset(runtime.GOARCH))
	item.Licence, item.SourceURL, item.Released = "GPL-2.0-only", base+supply.KernelSourceAsset(item.Version), "2026-09-28"
	path := filepath.Join(t.TempDir(), "kernel")
	if err := os.WriteFile(path, data, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	owner, err := supply.PrepareRuntime(t.Context(), item, file)
	if err != nil {
		if owner != nil {
			owner.Close()
		}
		t.Fatal(err)
	}
	defer owner.Close()
	if _, err := owner.File(); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("agents lease close fixture")
	agents := &kernelLeaseCloseFailure{err: failure}
	lease := &bootRuntimeLeaseV1{agents: agents, runtime: []*supply.VerifiedRuntime{owner}}
	if err := lease.Close(); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if _, err := owner.File(); !errors.Is(err, os.ErrClosed) {
		t.Fatal("agents failure retained an unowned kernel descriptor", err)
	}
	if err := lease.Close(); !errors.Is(err, failure) || agents.calls != 1 {
		t.Fatal("cleanup repeated or lost its original failure", err, agents.calls)
	}
}
