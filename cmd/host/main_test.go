package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/proto"
)

// The tests in this binary may start it again as a session's holder.
func TestMain(m *testing.M) { hosttest.Main(m) }

// A program in session A runs the helper's own rm, and restore, on session
// B. The helper refuses before it stops anything, and B keeps running.
func TestRmFromInsideASessionIsRefused(t *testing.T) {
	hosttest.New(t)
	b, err := host.New(host.NewArgs{Name: "b", Cwd: "~", Argv: []string{"sleep", "60"}})
	if err != nil {
		t.Fatal(err)
	}
	hosttest.InSession(t, "aaaa2222")
	var pe *proto.Error
	if err := runRm([]string{b.ID}); !errors.As(err, &pe) || pe.Code != "forbidden" || pe.Hint == "" {
		t.Fatalf("rm from inside a session = %v, want forbidden with a hint", err)
	}
	if err := runRestore([]string{"--all"}); !errors.As(err, &pe) || pe.Code != "forbidden" {
		t.Fatalf("restore from inside a session = %v, want forbidden", err)
	}
	list, err := host.List()
	if err != nil || len(list) != 1 || list[0].ID != b.ID || !list[0].Alive {
		t.Fatalf("after rm from inside a session: %+v, %v", list, err)
	}
}

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
