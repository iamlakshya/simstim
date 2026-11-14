//go:build linux

package microvm

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/wall"
)

// This models the consumer's native owner contract. The jail package tests
// the real creation pidfd separately. No VM, jail or namespace starts here.
type fakeVMProcess struct {
	mu        sync.Mutex
	physical  bool
	waitErr   error
	signalErr error
	waits     int
	signals   int
}

func (p *fakeVMProcess) Wait() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.waits++
	return p.waitErr
}

func (p *fakeVMProcess) Signal(os.Signal) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.signals++
	return p.signalErr
}

func (p *fakeVMProcess) PhysicalExitV1() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.physical
}

func (p *fakeVMProcess) proveExit() {
	p.mu.Lock()
	p.physical = true
	p.mu.Unlock()
}

func TestUncertainVMWaitRetainsResourcesWithoutJoiningLogEOF(t *testing.T) {
	waitErr := errors.New("owned native wait failed")
	process := &fakeVMProcess{waitErr: waitErr}
	lease, err := os.CreateTemp(t.TempDir(), "runtime-lease")
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	agents, err := os.CreateTemp(t.TempDir(), "agents-lease")
	if err != nil {
		t.Fatal(err)
	}
	defer agents.Close()
	logDone := make(chan struct{})
	var finishLog sync.Once
	t.Cleanup(func() { finishLog.Do(func() { close(logDone) }) })
	s := &sandbox{driver: &Driver{}, command: process, started: true, done: make(chan struct{}), logDone: logDone, lease: lease, agentsLease: agents}
	returned := make(chan struct{})
	go func() { s.waitProcess(process, nil); close(returned) }()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("a failed native wait blocked on a possibly live diagnostic writer")
	}
	if !errors.Is(s.Wait().Err, waitErr) || s.CleanExit() {
		t.Fatal("native uncertainty became a successful or clean exit")
	}
	for _, file := range []*os.File{lease, agents} {
		if _, err := file.Stat(); err != nil {
			t.Fatal("native uncertainty released a resource lease", err)
		}
	}
	if err := s.ReleasePreparedV1(); err == nil {
		t.Fatal("logical completion released the live preparation")
	}
	finishLog.Do(func() { close(logDone) })
	process.proveExit()
	if !s.refreshPhysicalExit() {
		t.Fatal("later physical proof from the same owner could not finish cleanup")
	}
	for _, file := range []*os.File{lease, agents} {
		if _, err := file.Stat(); err == nil {
			t.Fatal("proven physical exit retained a resource lease")
		}
	}
	if !errors.Is(s.Wait().Err, waitErr) || s.CleanExit() {
		t.Fatal("later physical proof erased the previous wait failure")
	}
	if process.waits != 1 {
		t.Fatal("cleanup restarted a completed logical wait")
	}
}

func TestVMKillRetriesOriginalOwnerAfterLogicalCompletion(t *testing.T) {
	waitErr := errors.New("original wait failure")
	signalErr := errors.New("original owner refused signal")
	process := &fakeVMProcess{waitErr: waitErr, signalErr: signalErr}
	s := &sandbox{command: process, started: true, done: make(chan struct{})}
	s.waitProcess(process, nil)
	if err := s.Kill(); !errors.Is(err, signalErr) {
		t.Fatal("kill skipped or replaced the original owner", err)
	}
	if process.signals != 1 || process.waits != 1 {
		t.Fatal("kill lost ownership or began an unbounded second wait")
	}
	if !errors.Is(s.Wait().Err, waitErr) || !errors.Is(s.Wait().Err, signalErr) {
		t.Fatal("cleanup lost native wait or signal errors")
	}
	process.proveExit()
	if !s.refreshPhysicalExit() || s.CleanExit() {
		t.Fatal("same-owner cleanup did not preserve an unclean result")
	}
}

func TestUnprovenVMDestroyPreservesSessionDisk(t *testing.T) {
	dir := t.TempDir()
	vmDir := filepath.Join(dir, "vm")
	if err := os.Mkdir(vmDir, 0o700); err != nil {
		t.Fatal(err)
	}
	disk := filepath.Join(vmDir, "disk.img")
	if err := os.WriteFile(disk, []byte("preserved session disk"), 0o600); err != nil {
		t.Fatal(err)
	}
	process := &fakeVMProcess{waitErr: errors.New("unproven owned exit")}
	s := &sandbox{spec: wall.Spec{SessionDir: dir}, command: process, started: true, done: make(chan struct{})}
	s.waitProcess(process, nil)
	if err := s.Destroy(); err == nil {
		t.Fatal("Destroy accepted logical completion as native exit")
	}
	if body, err := os.ReadFile(disk); err != nil || string(body) != "preserved session disk" {
		t.Fatal("unproven destruction removed or changed the session disk", err)
	}
	if process.signals != 1 {
		t.Fatal("destruction did not retry the same owned process")
	}
}
