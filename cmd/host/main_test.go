package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// The helper's own `new` reads a memory cap as the client does, and refuses a
// bad one with a hint.
func TestNewArgsMemoryMax(t *testing.T) {
	for size, want := range map[string]int64{"4G": 4 << 30, "4Gi": 4 << 30, "512MiB": 512 << 20, "auto": 0} {
		a, err := newArgs([]string{"--name", "x", "--memory-max", size, "--", "sleep", "60"})
		if err != nil || a.MemoryMax != want || a.Name != "x" || len(a.Argv) != 2 {
			t.Errorf("new --memory-max %s = %+v, %v, want a cap of %d", size, a, err, want)
		}
	}
	for _, size := range []string{"100M", "4096", "infinity", "4 gigs"} {
		_, err := newArgs([]string{"--memory-max", size})
		var pe *proto.Error
		if !errors.As(err, &pe) || pe.Code != "memory_max" || !strings.Contains(pe.Hint, api.MemorySizes) {
			t.Errorf("new --memory-max %q = %v, want a memory_max error with a hint", size, err)
		}
	}
}
