package host_test

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
)

type observedHolder struct {
	process *os.Process
	pid     int
	birth   string
	err     error
}

type holderObservation struct {
	mu       sync.Mutex
	attempts map[string][]observedHolder
}

// These tests change process-wide launch hooks and never run in parallel.
var observedHolders *holderObservation

// observeHolders records only process objects returned by this test's actual
// launch. Native birth information remains observation, never stop authority.
// No process table, command line or environment is scanned.
func observeHolders(t *testing.T) {
	t.Helper()
	oldStart, oldObserver := *host.StartHolder, observedHolders
	observer := &holderObservation{attempts: make(map[string][]observedHolder)}
	observedHolders = observer
	*host.StartHolder = func(m *host.Manifest) (*os.Process, error) {
		p, err := oldStart(m)
		entry := observedHolder{process: p}
		if p != nil {
			entry.pid = p.Pid
			entry.birth, _, entry.err = ownedHolderState(entry.pid)
			if entry.err == nil && entry.birth == "" {
				entry.err = errors.New("the owned holder had no native birth identity at launch")
			}
		} else {
			entry.err = errors.New("the test did not launch a direct owned holder")
		}
		observer.mu.Lock()
		observer.attempts[m.ID] = append(observer.attempts[m.ID], entry)
		observer.mu.Unlock()
		return p, err
	}
	t.Cleanup(func() { *host.StartHolder = oldStart; observedHolders = oldObserver })
}

func observedHolderEntries(t *testing.T, id string) []observedHolder {
	t.Helper()
	if observedHolders == nil {
		t.Fatal("this test did not install its owned holder observer")
	}
	observedHolders.mu.Lock()
	entries, ok := observedHolders.attempts[id]
	entries = append([]observedHolder(nil), entries...)
	observedHolders.mu.Unlock()
	if !ok {
		t.Fatalf("this test did not launch a holder for %s", id)
	}
	return entries
}

// namedHolderRuns checks physical launch lifetime, including before listen.
// SessionInfo.Alive describes a responding holder and cannot prove this.
func namedHolderRuns(t *testing.T, id string) bool {
	t.Helper()
	return len(holderPIDs(t, id)) > 0
}

func holderPIDs(t *testing.T, id string) []int {
	t.Helper()
	var pids []int
	for _, entry := range observedHolderEntries(t, id) {
		if entry.err != nil {
			t.Fatalf("observe the test's holder of %s: %v", id, entry.err)
		}
		birth, running, err := ownedHolderState(entry.pid)
		if err != nil {
			t.Fatal(err)
		}
		if birth == entry.birth && running {
			pids = append(pids, entry.pid)
		}
	}
	return pids
}

// This one failure fixture deliberately leaves stopProcess's Wait active.
// Signal its actual original process object, not a recovered numeric PID,
// then observe that same birth exit without a second Wait or bare PID kill.
func killObservedHolder(t *testing.T, pid int) {
	t.Helper()
	if observedHolders == nil {
		t.Fatal("this test did not install its owned holder observer")
	}
	observedHolders.mu.Lock()
	var found *observedHolder
	for _, entries := range observedHolders.attempts {
		for _, entry := range entries {
			if entry.pid == pid {
				entryCopy := entry
				found = &entryCopy
			}
		}
	}
	observedHolders.mu.Unlock()
	if found == nil || found.process == nil || found.err != nil {
		t.Fatalf("no original owned process object for %d", pid)
	}
	birth, running, err := ownedHolderState(pid)
	if err != nil {
		t.Fatal(err)
	}
	if birth != found.birth || !running {
		return
	}
	if err := found.process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		t.Fatal(fmt.Errorf("stop the owned fixture holder: %w", err))
	}
	hosttest.WaitFor(t, 5*time.Second, "the owned fixture holder to exit", func() bool {
		birth, running, err := ownedHolderState(pid)
		if err != nil {
			t.Fatal(err)
		}
		return birth != found.birth || !running
	})
}
