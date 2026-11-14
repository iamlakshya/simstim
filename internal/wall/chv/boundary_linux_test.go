//go:build linux

package chv

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestMicroVMBoundaryChecksWholeReserveAndNoSwap(t *testing.T) {
	budget, err := BudgetFor(1024 * mebibyte)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	write := func(memory, swap string) {
		t.Helper()
		for name, value := range map[string]string{"memory.max": memory, "memory.swap.max": swap} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(value+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	write(strconv.FormatInt(budget.Limit, 10), "0")
	if err := verifyMemoryBoundary(context.Background(), root, budget); err != nil {
		t.Fatal(err)
	}
	for _, values := range [][2]string{{strconv.FormatInt(budget.Guest, 10), "0"}, {"max", "0"}, {strconv.FormatInt(budget.Limit, 10), "max"}, {strconv.FormatInt(budget.Limit, 10), "4096"}} {
		write(values[0], values[1])
		if err := verifyMemoryBoundary(context.Background(), root, budget); err == nil {
			t.Fatalf("incorrect memory boundary accepted: %v", values)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := verifyMemoryBoundary(ctx, root, budget); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled boundary check: %v", err)
	}
}
