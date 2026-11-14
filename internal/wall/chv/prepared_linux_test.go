//go:build linux

package chv

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/iamlakshya/simstim/internal/wall"
)

func TestReleasePreparedPreservesDiskAndFencesUnstartedFactory(t *testing.T) {
	dir := t.TempDir()
	disk := filepath.Join(dir, "disk.img")
	if err := os.WriteFile(disk, []byte("existing private disk"), 0o600); err != nil {
		t.Fatal(err)
	}
	lease, err := os.CreateTemp(t.TempDir(), "owned-lease")
	if err != nil {
		t.Fatal(err)
	}
	s := &sandbox{lease: lease, done: make(chan struct{}), spec: wall.Spec{SessionDir: dir, VM: &wall.VMSpec{GuestRAM: 1 << 30}}, driver: &Driver{options: Options{Boundary: func(context.Context, MemoryBudget) error {
		t.Fatal("discarded prepared factory reached its first host boundary")
		return errors.New("discarded factory")
	}}}}
	if err := s.ReleasePreparedV1(); err != nil {
		t.Fatal(err)
	}
	if _, err := lease.Stat(); err == nil {
		t.Fatal("discarded factory kept its native lease descriptor open")
	}
	if _, err := s.Start(t.Context()); err == nil {
		t.Fatal("discarded factory reached runtime startup")
	}
	if err := s.ReleasePreparedV1(); err != nil {
		t.Fatal("releasing the same owned preparation twice failed", err)
	}
	if body, err := os.ReadFile(disk); err != nil || string(body) != "existing private disk" {
		t.Fatal("releasing a preparation removed or changed its disk", err)
	}
}

func TestReleasePreparedRefusesLiveFactoryLease(t *testing.T) {
	lease, err := os.CreateTemp(t.TempDir(), "owned-lease")
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	s := &sandbox{lease: lease, started: true, done: make(chan struct{})}
	if err := s.ReleasePreparedV1(); err == nil {
		t.Fatal("observer cleanup released a live runtime's native lease")
	}
	if _, err := lease.Stat(); err != nil {
		t.Fatal("live runtime lost its lease", err)
	}
	close(s.done) // Unit fixture for the driver's already-owned completion.
	if err := s.ReleasePreparedV1(); err != nil {
		t.Fatal("completed owned runtime could not release its lease", err)
	}
	if _, err := lease.Stat(); err == nil {
		t.Fatal("completed factory kept its native lease")
	}
}
