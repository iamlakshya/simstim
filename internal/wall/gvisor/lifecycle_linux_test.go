//go:build linux

package gvisor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/iamlakshya/simstim/internal/wall"
)

// The lifecycle regression invokes no command, namespace, mount or runtime.
// It models a wait error that has not proved physical exit, then the SAME
// owner's later positive exit evidence. Saved PID reconstruction cannot pass.
type uncertainCommand struct {
	mu      sync.Mutex
	closed  bool
	signals int
}

func (*uncertainCommand) Start() error { return nil }
func (*uncertainCommand) Wait() error  { return errors.New("wait uncertain") }
func (c *uncertainCommand) Signal(os.Signal) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.signals++
	return nil
}
func (*uncertainCommand) ExitCode() int          { return -1 }
func (c *uncertainCommand) PhysicalExitV1() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.closed }

func TestUncertainRunscWaitRetainsRuntimeAndSessionData(t *testing.T) {
	dir := t.TempDir()
	spec := wall.Spec{ID: "session", SessionDir: dir}
	storage, err := prepareStorage(context.Background(), spec, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"manifest.json", "work", "state"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	command := &uncertainCommand{}
	s := &sandbox{spec: spec, storage: storage, command: command, started: true, runtime: &Runtime{}, done: make(chan struct{})}
	s.reap(command)
	if result := s.Wait(); result.Err == nil {
		t.Fatal("uncertain wait succeeded")
	}
	if s.CleanExit() {
		t.Fatal("uncertain wait became clean")
	}
	if err := s.ReleasePreparedV1(); err == nil {
		t.Fatal("released uncertain runtime")
	}
	if err := s.Destroy(); err == nil {
		t.Fatal("destroyed uncertain runtime")
	}
	if _, err := os.Stat(filepath.Join(dir, "gvisor", "session")); err != nil {
		t.Fatal("runtime disappeared", err)
	}
	if err := s.Kill(); err == nil {
		t.Fatal("kill discarded prior wait error")
	}
	command.mu.Lock()
	command.closed = true
	command.mu.Unlock()
	if err := s.Kill(); err == nil {
		t.Fatal("positive later exit erased prior failure")
	}
	if s.CleanExit() {
		t.Fatal("later closure became a clean run")
	}
	if err := s.Destroy(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"manifest.json", "work", "state"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatal("session data removed", name, err)
		}
	}
}

func TestRuntimeDestroyRefusesSubstitutedDirectory(t *testing.T) {
	dir := t.TempDir()
	spec := wall.Spec{ID: "session", SessionDir: dir}
	storage, err := prepareStorage(context.Background(), spec, false)
	if err != nil {
		t.Fatal(err)
	}
	defer storage.release()
	if err := os.Rename(filepath.Join(dir, "gvisor"), filepath.Join(dir, "original")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "gvisor"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gvisor", "sentinel"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := storage.destroy(); err == nil {
		t.Fatal("deleted substituted runtime directory")
	}
	if data, err := os.ReadFile(filepath.Join(dir, "gvisor", "sentinel")); err != nil || string(data) != "keep" {
		t.Fatal("replacement changed", err)
	}
}

func TestRuntimeFailureKeepsItsClassBeforeAnyLaunch(t *testing.T) {
	for _, code := range []string{"runtime_missing", "runtime_advisory"} {
		t.Run(code, func(t *testing.T) {
			cause := failure(code, "runtime refused", nil)
			driver := &Driver{options: Options{ExpectedRuntime: func(context.Context) (supply.Manifest, error) { return supply.Manifest{}, cause }}}
			s := &sandbox{driver: driver, owner: context.Background(), done: make(chan struct{})}
			_, err := s.Start(context.Background())
			var diagnostic *proto.Error
			if !errors.As(err, &diagnostic) || diagnostic.Code != code || diagnostic.Hint == "" {
				t.Fatalf("runtime refusal lost its class/hint: %v", err)
			}
			if !s.physical || s.Wait().Err == nil || s.command != nil {
				t.Fatal("invalid runtime launched or forgot its failure")
			}
		})
	}
}
