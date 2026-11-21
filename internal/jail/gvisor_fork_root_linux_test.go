//go:build linux

package jail

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
	"golang.org/x/sys/unix"
)

func forkRootModelV2(t *testing.T) (*gvisorForkRootOwnerV2, *os.Root) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR", "XDG_CONFIG_DIRS", "XDG_DATA_DIRS"} {
		name := filepath.Join(dir, key)
		if err := os.Mkdir(name, 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv(key, name)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	file, err := root.Open(".")
	if err != nil {
		t.Fatal(err)
	}
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	owner := &gvisorForkRootOwnerV2{command: &Cmd{}, root: file, rootInfo: info, expected: make(map[string]*os.File)}
	for _, name := range []string{"work", "home/agent", "home/agent/.state"} {
		if err := root.MkdirAll(name, 0o700); err != nil {
			t.Fatal(err)
		}
		actual, err := root.Open(name)
		if err != nil {
			t.Fatal(err)
		}
		owner.expected["/"+name] = actual
	}
	t.Cleanup(func() { _ = owner.close(); _ = root.Close() })
	// This is only an inode-comparison model. It deliberately lacks the real
	// readonly tmpfs/mapped-A issuer, ready flag and original native Cmd chain.
	return owner, root
}

func TestGvisorForkReceiverRejectsEverySubstitutedBackingRole(t *testing.T) {
	for _, name := range []string{"work", "home/agent", "home/agent/.state"} {
		t.Run(name, func(t *testing.T) {
			owner, root := forkRootModelV2(t)
			if err := owner.checkMountsLockedV2(); err != nil {
				t.Fatal(err)
			}
			if err := root.Rename(name, "held-"+strings.ReplaceAll(name, "/", "-")); err != nil {
				t.Fatal(err)
			}
			if err := root.Mkdir(name, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := owner.checkMountsLockedV2(); err == nil {
				t.Fatal("a same-name work/home/state replacement passed original native backing checks")
			}
		})
	}
}

func TestGvisorForkRootRejectsAnOrdinaryDirectoryAndUnissuedModel(t *testing.T) {
	owner, _ := forkRootModelV2(t)
	if err := validateGvisorMountedRootV2(owner.root); err == nil {
		t.Fatal("an ordinary directory became the original mapped-A readonly tmpfs root")
	}
	if err := owner.checkReadyV2(); err == nil {
		t.Fatal("inode-comparison data issued a mounted-root native capability")
	}
}

func TestGvisorForkRootRoleNeverEntersSerialisedSetup(t *testing.T) {
	owner, _ := forkRootModelV2(t)
	value := setup{GvisorV1Version: 1, gvisorForkRoot: owner.root}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "fork_root") || strings.Contains(string(data), "gvisorForkRoot") || strings.Contains(string(data), owner.root.Name()) {
		t.Fatal("the parent-only mounted-root loan entered public C setup data")
	}
	var decoded setup
	if err := json.Unmarshal(data, &decoded); err != nil || decoded.gvisorForkRoot != nil {
		t.Fatal("setup data reconstructed the mounted-root native loan", err)
	}
	value.Files, value.StatusFD = 1, 5
	before, err := expectedGvisorSlotsV1(&value, stageInit)
	if err != nil {
		t.Fatal(err)
	}
	value.gvisorForkRoot = nil
	after, err := expectedGvisorSlotsV1(&value, stageInit)
	if err != nil || !slices.Equal(before, after) {
		t.Fatal("the parent-only root changed the exact public child role vector", err)
	}
}

func TestGvisorMountedRootReportRejectsLegacyMissingAndExtraRoles(t *testing.T) {
	for _, test := range []struct {
		phase string
		count int
	}{{"nested-created", 4}, {"nested-created", 5}, {"nested-created-v2", 4}, {"nested-created-v2", 6}} {
		t.Run(test.phase+"-"+string(rune('0'+test.count)), func(t *testing.T) {
			pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
			if err != nil {
				t.Fatal(err)
			}
			connections := make([]*net.UnixConn, 0, 2)
			for _, fd := range pair {
				file := os.NewFile(uintptr(fd), "root-report-protocol-fixture")
				connection, err := net.FileConn(file)
				closeErr := file.Close()
				if err != nil || closeErr != nil {
					t.Fatal(err, closeErr)
				}
				actual := connection.(*net.UnixConn)
				if err := proto.EnableUnixCredentialsV1(actual); err != nil {
					t.Fatal(err)
				}
				if err := actual.SetDeadline(time.Now().Add(time.Second)); err != nil {
					t.Fatal(err)
				}
				connections = append(connections, actual)
				t.Cleanup(func() { _ = actual.Close() })
			}
			files := make([]*os.File, 0, test.count)
			for range test.count {
				file, err := os.Open("/dev/null")
				if err != nil {
					t.Fatal(err)
				}
				files = append(files, file)
				t.Cleanup(func() { _ = file.Close() })
			}
			if err := sendGvisorV2(connections[0], gvisorMessageV2{Phase: test.phase}, files); err != nil {
				t.Fatal(err)
			}
			peer := unix.Ucred{Pid: int32(os.Getpid()), Uid: uint32(os.Getuid()), Gid: uint32(os.Getgid())}
			_, received, err := receiveGvisorV2(connections[1], peer, "nested-created-v2", 5)
			if err == nil || len(received) != 0 {
				closeFiles(received)
				t.Fatal("an old or changed report vector entered the exact mounted-root role", err)
			}
		})
	}
}

func TestGvisorForkRootCloseJoinsReadersBeforeDescriptorClosure(t *testing.T) {
	owner, _ := forkRootModelV2(t)
	owner.borrowers.Add(1)
	released := false
	defer func() {
		if !released {
			owner.borrowers.Done()
		}
	}()
	closed := make(chan error, 1)
	go func() { closed <- owner.close() }()
	deadline := time.Now().Add(time.Second)
	for {
		owner.mu.Lock()
		closing := owner.closing
		owner.mu.Unlock()
		if closing {
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatal("the original root close never fenced new borrowers")
		}
		select {
		case err := <-closed:
			t.Fatal("close released a root before its original reader joined", err)
		case <-time.After(time.Millisecond):
		}
	}
	if _, err := unix.FcntlInt(owner.root.Fd(), unix.F_GETFD, 0); err != nil {
		t.Fatal("a root descriptor closed during an original borrower", err)
	}
	owner.borrowers.Done()
	released = true
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("the original root close did not join its reader")
	}
}

func TestGvisorForkBorrowRejectsAnExitedOriginalCreation(t *testing.T) {
	owner, _ := forkRootModelV2(t)
	pidfd := -1
	command := exec.Command("/bin/true")
	command.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C"}
	command.SysProcAttr = &syscall.SysProcAttr{PidFD: &pidfd}
	if err := command.Run(); err != nil {
		t.Fatal(err)
	}
	if pidfd < 0 {
		t.Fatal("the original creation returned no atomic pidfd")
	}
	defer unix.Close(pidfd)
	actual := &Cmd{ctx: t.Context(), phase: phaseRunning, owned: true, pid: command.Process.Pid, pidfd: pidfd, gvisorOuter: &gvisorOuterOwnerV1{fork: owner}}
	called := false
	err := actual.WithGvisorForkRootsV2(t.Context(), func(*os.Root, *os.Root) error { called = true; return nil })
	if err == nil || called {
		t.Fatal("an exited original creation issued fork reads")
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Fatal("the original native death was replaced by an ad-hoc process-path lookup")
	}
}
