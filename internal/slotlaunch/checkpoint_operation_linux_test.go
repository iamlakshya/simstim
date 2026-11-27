//go:build linux

package slotlaunch

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestCheckpointTransportCannotMintSourceFromMissingRoles(t *testing.T) {
	for _, roles := range [][]*os.File{nil, make([]*os.File, 13), make([]*os.File, 17)} {
		owner, err := RunCheckpointOperationV1(t.Context(), roles, CheckpointOperationV1{Session: "abc234de", Action: "list"})
		if err == nil || owner != nil {
			t.Fatal("missing native operands created an actual root operation")
		}
	}
	var owner CheckpointOperationOwnerV1
	called := false
	if err := owner.WithFilesV1(t.Context(), func([]*os.File) error { called = true; return nil }); err == nil || called {
		t.Fatal("a zero operation issued root outputs")
	}
}

func TestCheckpointRootRefusalsKeepStructuredOffAndLowDiskCodes(t *testing.T) {
	for _, code := range []string{"rewind_off", "checkpoint_low_disk"} {
		err := checkpointRemoteErrorV1(Response{Code: code, Error: "checkpoints paused", Hint: "Free space on the original checkpoint filesystem."})
		var wire *proto.Error
		if !errors.As(err, &wire) || wire.Code != code || wire.Hint == "" {
			t.Fatal("the original root refusal lost its structured status", err)
		}
	}
}

func TestSlotAllocationNoRoomKeepsItsNativeCodeThroughWrappers(t *testing.T) {
	known := &launchError{Code: "no_room", Message: "no slot is available", Hint: "Stop one sandbox, then try again."}
	err := errors.Join(known, io.ErrClosedPipe)
	var wire *proto.Error
	if !errors.As(err, &wire) || wire.Code != "no_room" || wire.Message != known.Message || wire.Hint != known.Hint {
		t.Fatal("native slot refusal collapsed into a generic boot error", err)
	}
	var original *launchError
	if !errors.As(err, &original) || original != known {
		t.Fatal("root-side refusal classification lost its original error")
	}
}

func TestCheckpointNativeWorkerFixtureV1(t *testing.T) {
	if os.Getenv(brand.Env("CHECKPOINT_NATIVE_WORKER_FIXTURE")) != "1" {
		return
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}

// This ordinary child supplies a real kernel birth handle for the cleanup
// seam only. It is not root and cannot issue a slot Source or checkpoint.
func TestCheckpointOperationCloseWaitsForActualWorkerExit(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, "-test.run=^TestCheckpointNativeWorkerFixtureV1$")
	command.Env = append(os.Environ(), brand.Env("CHECKPOINT_NATIVE_WORKER_FIXTURE")+"=1")
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	pidfd := -1
	command.SysProcAttr = &syscall.SysProcAttr{PidFD: &pidfd}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	joined := false
	t.Cleanup(func() {
		input.Close()
		if !joined {
			command.Process.Kill()
			command.Wait()
		}
	})
	if pidfd < 0 {
		t.Fatal("the actual child has no kernel creation handle")
	}
	worker := os.NewFile(uintptr(pidfd), "actual-checkpoint-cleanup-worker")
	output, err := os.CreateTemp(t.TempDir(), "actual-root-output-role")
	if err != nil {
		worker.Close()
		t.Fatal(err)
	}
	owner := &CheckpointOperationOwnerV1{worker: worker, files: []*os.File{output}}
	closed := make(chan error, 1)
	go func() { closed <- owner.Close() }()
	select {
	case err := <-closed:
		t.Fatalf("a live original worker was treated as closed: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	if _, err := output.Stat(); err != nil {
		t.Fatal("a possibly live worker lost its actual output owner", err)
	}
	if err := input.Close(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatal(err)
	}
	joined = true
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if _, err := output.Stat(); err == nil {
		t.Fatal("a completed worker retained its output descriptor")
	}
}
