//go:build linux

package jail

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const readonlyThreadChildV2 = "JAIL_READONLY_THREAD_CAPS_CHILD_V2"

// The restriction runs only in this fresh child user namespace. This test
// proves all-thread and late-thread restriction; it grants no original VMM,
// actor, OP, creator or physical-closure authority from its test credentials.
func TestReadonlyCapsCoverExistingAndLateNativeGoThreadsV2(t *testing.T) {
	if os.Getenv(readonlyThreadChildV2) == "1" {
		checkReadonlyThreadChildV2(t)
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "-test.run=^TestReadonlyCapsCoverExistingAndLateNativeGoThreadsV2$")
	command.Env = append(os.Environ(), readonlyThreadChildV2+"=1")
	command.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: unix.CLONE_NEWUSER, Pdeathsig: unix.SIGKILL,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
		AmbientCaps: initCaps,
	}
	output, err := command.CombinedOutput()
	if errors.Is(err, unix.EPERM) || errors.Is(err, unix.EACCES) {
		t.Skip("this test host does not admit the fresh native user namespace")
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 42 && strings.Contains(string(output), "ALLTHREADS_UNSUPPORTED_V2") {
		t.Skip("this binary cannot account for all native Go threads; the production route refuses")
	}
	if err != nil {
		t.Fatalf("the native thread restriction child failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "ALLTHREADS_LATE_ZERO_V2") {
		t.Fatal("the actual late-thread status check did not complete")
	}
}

func checkReadonlyThreadChildV2(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	worker := make(chan int, 1)
	release := make(chan struct{})
	defer close(release)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		worker <- unix.Gettid()
		<-release
	}()
	first := <-worker
	before, err := readonlyThreadStatusV2(first)
	if err != nil {
		t.Fatal(err)
	}
	if before["CapPrm:"]&(uint64(1)<<unix.CAP_SETPCAP) == 0 || before["CapBnd:"] == 0 {
		t.Fatal("the original worker had no native setup capabilities to clear")
	}
	if err := readonlyAllThreadsPrctlV2(unix.PR_SET_NO_NEW_PRIVS, 1); err != nil {
		if errors.Is(err, syscall.ENOTSUP) {
			fmt.Println("ALLTHREADS_UNSUPPORTED_V2")
			os.Exit(42)
		}
		t.Fatal(err)
	}
	for capability := 0; capability < 64; capability++ {
		err := readonlyAllThreadsPrctlV2(unix.PR_CAPBSET_DROP, uintptr(capability))
		if errors.Is(err, unix.EINVAL) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := dropReadonlyAllThreadCapsV2(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir("/proc/self/task")
	if err != nil || len(entries) == 0 || len(entries) > 128 {
		t.Fatal("the original native thread set is unavailable or exceeds its bound", err)
	}
	existing := make(map[int]bool, len(entries))
	for _, entry := range entries {
		tid, err := strconv.Atoi(entry.Name())
		if err != nil || tid <= 0 {
			t.Fatal("the original native thread has an invalid identity")
		}
		existing[tid] = true
		assertReadonlyThreadZeroV2(t, tid)
	}
	// Hold more simultaneously locked workers than all existing OS threads.
	// At least one observed native TID must therefore be born after the drops.
	late := make(chan int, len(existing)+2)
	for range len(existing) + 2 {
		go func() {
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
			late <- unix.Gettid()
			<-release
		}()
	}
	fresh := false
	for range len(existing) + 2 {
		tid := <-late
		fresh = fresh || !existing[tid]
		assertReadonlyThreadZeroV2(t, tid)
	}
	if !fresh {
		t.Fatal("no actual later native thread was observed")
	}
	fmt.Println("ALLTHREADS_LATE_ZERO_V2")
}

func assertReadonlyThreadZeroV2(t *testing.T, tid int) {
	t.Helper()
	status, err := readonlyThreadStatusV2(tid)
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range status {
		want := uint64(0)
		if key == "NoNewPrivs:" {
			want = 1
		}
		if value != want {
			t.Fatalf("actual native thread %d retained %s=%x", tid, key, value)
		}
	}
}

func readonlyThreadStatusV2(tid int) (map[string]uint64, error) {
	data, err := os.ReadFile("/proc/self/task/" + strconv.Itoa(tid) + "/status")
	if err != nil || len(data) > 16<<10 {
		return nil, errors.Join(err, fmt.Errorf("the actual thread status is unavailable or oversized"))
	}
	status := map[string]uint64{}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		key := fields[0]
		if key != "CapInh:" && key != "CapPrm:" && key != "CapEff:" && key != "CapBnd:" && key != "CapAmb:" && key != "NoNewPrivs:" {
			continue
		}
		if _, seen := status[key]; seen || len(fields) != 2 {
			return nil, fmt.Errorf("the actual thread status repeats or changes a restriction field")
		}
		base := 16
		if key == "NoNewPrivs:" {
			base = 10
		}
		value, err := strconv.ParseUint(fields[1], base, 64)
		if err != nil {
			return nil, err
		}
		status[key] = value
	}
	if len(status) != 6 {
		return nil, fmt.Errorf("the actual thread restriction status is incomplete")
	}
	return status, nil
}
