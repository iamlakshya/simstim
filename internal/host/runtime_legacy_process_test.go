package host_test

import (
	"io"
	"os"
	"os/exec"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
)

func TestRecordedHolderMatchesItsOwnSession(t *testing.T) {
	hosttest.New(t)
	cmd := legacyHolderProcess(t)
	if !host.HolderPIDAlive(cmd.Process.Pid, fakeID) {
		t.Fatal("the holder-shaped child was not recognised")
	}
	if host.HolderPIDAlive(cmd.Process.Pid, "efgh2345") {
		t.Fatal("another session borrowed the recorded process")
	}
	other := processWithHolderArgs(t, "other-command", fakeID)
	if host.HolderPIDAlive(other.Process.Pid, fakeID) {
		t.Fatal("an unrelated command with a matching argument counted as holder")
	}
}

// legacyHolderProcess runs a child with the same argv as a holder, without
// taking holder.lock or opening the socket served by the test's older holder.
func legacyHolderProcess(t *testing.T) *exec.Cmd {
	t.Helper()
	return processWithHolderArgs(t, "holder", fakeID)
}

func processWithHolderArgs(t *testing.T, command, id string) *exec.Cmd {
	t.Helper()
	readyR, readyW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = readyR.Close(); _ = readyW.Close() })
	waitR, waitW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = waitR.Close(); _ = waitW.Close() })
	cmd := exec.Command(os.Args[0], command, id)
	cmd.Env = append(os.Environ(), brand.Env("TEST_LEGACY_PID")+"=1")
	cmd.ExtraFiles = []*os.File{readyW, waitR}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	_ = readyW.Close()
	_ = waitR.Close()
	var one [1]byte
	if _, err := io.ReadFull(readyR, one[:]); err != nil {
		t.Fatalf("holder-shaped child was not ready: %v", err)
	}
	return cmd
}
