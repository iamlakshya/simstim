//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/proto"
)

// This source regression runs the ACTUAL placement lock, native original OP
// and pinned first writer in two separate processes. It starts no agent,
// namespace, manager or provider. Only the machine budget/caller fixture is
// synthetic; unit absence still comes from the real readonly cgroup2 reader.
func init() {
	if os.Getenv(brand.Env("TEST_MEMORY_RESERVATION_CHILD")) != "1" {
		return
	}
	if len(os.Args) != 2 || !validID(os.Args[1]) {
		os.Exit(2)
	}
	os.Exit(memoryReservationRaceChildV1(os.Args[1]))
}

type memoryRaceResultV1 struct {
	Saved bool   `json:"saved"`
	Code  string `json:"code,omitempty"`
	Error string `json:"error,omitempty"`
}

func memoryReservationRaceChildV1(id string) int {
	procRoot = os.Getenv(brand.Env("TEST_MEMORY_PROC"))
	SelfCgroup = func() ([]byte, error) {
		return []byte(fmt.Sprintf("0::/user.slice/user-%d.slice/session-reservation-test.scope\n", os.Getuid())), nil
	}
	CgroupNamespaces = func() (string, string, error) { return "cgroup:[reservation-fixture]", "", nil }
	ready, release := os.NewFile(3, "reservation-race-ready"), os.NewFile(4, "reservation-race-release")
	defer ready.Close()
	defer release.Close()
	result := memoryRaceResultV1{}
	if err := func() error {
		reservation, err := reserveSessionID(id)
		if err != nil {
			return err
		}
		operation, err := beginReservedSessionOperation(context.Background(), reservation)
		if err != nil {
			reservation.close()
			return err
		}
		defer func() { reservation.close(); operation.Close() }()
		if _, err := ready.Write([]byte{1}); err != nil {
			return err
		}
		var one [1]byte
		if _, err := io.ReadFull(release, one[:]); err != nil {
			return err
		}
		now := time.Now().UTC()
		manifest := &Manifest{Schema: 1, ID: id, Name: id, CreatedAt: now,
			Run: Runtime{BootID: BootID()}, Status: Status{State: StateStarting, Since: now},
			Limits: Limits{MemoryMax: 256 << 20, Declared: 256 << 20}, Wall: &WallRecord{Level: "process", Driver: "process"}}
		err = saveReservedManifest(operation, manifest)
		result.Saved = err == nil
		if err != nil {
			var supplied *proto.Error
			if errors.As(err, &supplied) {
				result.Code = supplied.Code
			}
			result.Error = err.Error()
		}
		if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
			return err
		}
		// Keep both original operations/directories live until the parent has
		// observed both results. An early successful cleanup cannot hide the
		// first reservation from the competing native process.
		_, err = io.ReadFull(release, one[:])
		return err
	}(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func TestTwoNativePublicationProcessesCannotOvercommitDeclaredRoom(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	run := filepath.Join(root, "run")
	proc := filepath.Join(root, "proc")
	for _, name := range []string{home, run, proc, filepath.Join(root, "config-dirs"), filepath.Join(root, "data-dirs")} {
		if err := os.MkdirAll(name, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// 800 MiB leaves less than two 256-MiB reservations after the unchanged
	// system reserve. The test never allocates those caps or exceeds512MiB.
	if err := os.WriteFile(filepath.Join(proc, "meminfo"), []byte("MemTotal:       819200 kB\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		t.Fatal(err)
	}
	bootDir := filepath.Join(proc, "sys", "kernel", "random")
	if err := os.MkdirAll(bootDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bootDir, "boot_id"), boot, 0o600); err != nil {
		t.Fatal(err)
	}
	install := filepath.Join(home, ".local", "share", brand.Name, "bin", "reservation-fixture")
	if err := os.MkdirAll(install, 0o700); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(executable)
	if err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(install, brand.HostBinary)
	out, err := os.OpenFile(helper, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		in.Close()
		t.Fatal(err)
	}
	_, copyErr := io.Copy(out, in)
	if err := errors.Join(copyErr, in.Close(), out.Close()); err != nil {
		t.Fatal(err)
	}
	env := []string{}
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if key == "HOME" || key == "USERPROFILE" || strings.HasPrefix(key, "XDG_") || key == brand.Env("TEST_MEMORY_RESERVATION_CHILD") || key == brand.Env("TEST_MEMORY_PROC") {
			continue
		}
		env = append(env, value)
	}
	env = append(env, "HOME="+home, "USERPROFILE="+home, "XDG_CONFIG_HOME="+filepath.Join(home, ".config"), "XDG_DATA_HOME="+filepath.Join(home, ".local", "share"), "XDG_STATE_HOME="+filepath.Join(home, ".local", "state"), "XDG_CACHE_HOME="+filepath.Join(home, ".cache"), "XDG_RUNTIME_DIR="+run, "XDG_CONFIG_DIRS="+filepath.Join(root, "config-dirs"), "XDG_DATA_DIRS="+filepath.Join(root, "data-dirs"), brand.Env("TEST_MEMORY_RESERVATION_CHILD")+"=1", brand.Env("TEST_MEMORY_PROC")+"="+proc)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type worker struct {
		cmd            *exec.Cmd
		ready, release *os.File
		result         io.ReadCloser
	}
	var workers []worker
	defer func() {
		cancel()
		for _, worker := range workers {
			_ = worker.release.Close()
			_ = worker.ready.Close()
			_ = worker.result.Close()
			if worker.cmd.ProcessState == nil {
				_ = worker.cmd.Wait()
			}
		}
	}()
	for _, id := range []string{"aaaa2222", "bbbb3333"} {
		readyR, readyW, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		releaseR, releaseW, err := os.Pipe()
		if err != nil {
			readyR.Close()
			readyW.Close()
			t.Fatal(err)
		}
		cmd := exec.CommandContext(ctx, helper, id)
		cmd.Env, cmd.ExtraFiles = env, []*os.File{readyW, releaseR}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		_ = readyW.Close()
		_ = releaseR.Close()
		workers = append(workers, worker{cmd: cmd, ready: readyR, release: releaseW, result: stdout})
	}
	for _, worker := range workers {
		var one [1]byte
		if _, err := io.ReadFull(worker.ready, one[:]); err != nil {
			t.Fatal(err)
		}
	}
	for _, worker := range workers {
		if _, err := worker.release.Write([]byte{1}); err != nil {
			t.Fatal(err)
		}
	}
	saved, noRoom := 0, 0
	for _, worker := range workers {
		var result memoryRaceResultV1
		if err := json.NewDecoder(worker.result).Decode(&result); err != nil {
			t.Fatal(err)
		}
		if result.Saved {
			saved++
		} else if result.Code == "no_room" {
			noRoom++
		} else {
			t.Fatalf("unexpected native publication result: %+v", result)
		}
	}
	if saved != 1 || noRoom != 1 {
		t.Fatalf("both processes crossed admission: saved=%d no_room=%d", saved, noRoom)
	}
	for _, worker := range workers {
		if _, err := worker.release.Write([]byte{1}); err != nil {
			t.Fatal(err)
		}
	}
	for _, worker := range workers {
		if err := worker.cmd.Wait(); err != nil {
			t.Fatal(err)
		}
	}
}
