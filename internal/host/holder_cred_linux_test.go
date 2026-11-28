//go:build linux

package host

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"golang.org/x/sys/unix"
)

func TestAllThreadsNoNewPrivs(t *testing.T) {
	if os.Getenv("TEST_ALL_THREADS_NNP") == "1" {
		const extraThreads = 4
		ready := make(chan struct{}, extraThreads)
		release := make(chan struct{})
		var wg sync.WaitGroup
		for range extraThreads {
			wg.Add(1)
			go func() {
				defer wg.Done()
				runtime.LockOSThread()
				defer runtime.UnlockOSThread()
				ready <- struct{}{}
				<-release
			}()
		}
		for range extraThreads {
			<-ready
		}
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if err := setHolderNoNewPrivs(); err != nil {
			close(release)
			wg.Wait()
			t.Fatalf("setHolderNoNewPrivs: %v", err)
		}
		if holderTestCGOEnabled {
			// Under -race (cgo), Go's AllThreadsSyscall6 returns ENOTSUP and
			// falls back to unix.Prctl on the calling thread.
			statusPath := fmt.Sprintf("/proc/self/task/%d/status", unix.Gettid())
			b, err := os.ReadFile(statusPath)
			close(release)
			wg.Wait()
			if err != nil {
				t.Fatalf("read %s: %v", statusPath, err)
			}
			if !strings.Contains(string(b), "NoNewPrivs:\t1") {
				t.Fatalf("calling thread missing NoNewPrivs:\\t1:\n%s", string(b))
			}
			return
		}
		tasks, err := os.ReadDir("/proc/self/task")
		if err != nil {
			close(release)
			wg.Wait()
			t.Fatalf("read /proc/self/task: %v", err)
		}
		if len(tasks) < extraThreads+1 {
			close(release)
			wg.Wait()
			t.Fatalf("expected at least %d OS threads, saw %d", extraThreads+1, len(tasks))
		}
		for _, de := range tasks {
			b, err := os.ReadFile(filepath.Join("/proc/self/task", de.Name(), "status"))
			if err != nil {
				continue
			}
			if !strings.Contains(string(b), "NoNewPrivs:\t1") {
				close(release)
				wg.Wait()
				t.Fatalf("thread %s missing NoNewPrivs:\\t1:\n%s", de.Name(), string(b))
			}
		}
		close(release)
		wg.Wait()
		return
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestAllThreadsNoNewPrivs$")
	cmd.Env = append(os.Environ(), "TEST_ALL_THREADS_NNP=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("subprocess failed: %v\n%s", err, string(out))
	}
}

func TestPlaceGuestAgentInCgroupWritesProcs(t *testing.T) {
	dir := t.TempDir()
	procs := filepath.Join(dir, "cgroup.procs")
	placeGuestAgentInCgroup(4242, &guestConfig{agentCgroup: dir})
	b, err := os.ReadFile(procs)
	if err != nil {
		t.Fatalf("read cgroup.procs: %v", err)
	}
	if got := strings.TrimSpace(string(b)); got != "4242" {
		t.Fatalf("cgroup.procs = %q, want %q", got, "4242")
	}
}
