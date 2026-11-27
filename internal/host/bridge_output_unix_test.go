//go:build linux || darwin

package host

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
	"golang.org/x/sys/unix"
)

func TestBridgeOutputProcessHelper(t *testing.T) {
	if len(os.Args) == 0 || os.Args[len(os.Args)-1] != "bridge-output-fixture" {
		return
	}
	if err := RunProcessBridgeOutput(os.Stdout); err != nil {
		os.Exit(2)
	}
	os.Exit(0) // no test-runner text may follow the byte stream
}

func TestBridgeOutputParentFixture(t *testing.T) {
	if len(os.Args) == 0 || os.Args[len(os.Args)-1] != "bridge-parent-fixture" {
		return
	}
	executable, err := os.Executable()
	if err != nil {
		os.Exit(2)
	}
	death := os.NewFile(3, "child-lifetime")
	owner, err := startBridgeOutput(executable, []string{"-test.run=^TestBridgeOutputProcessHelper$", "--", "bridge-output-fixture"}, os.Stdout, death)
	death.Close() // only the output child keeps the lifetime pipe after Start
	if err != nil {
		os.Exit(2)
	}
	_, _ = owner.Write(bytes.Repeat([]byte{'x'}, proto.MaxFrame-1024))
	_ = owner.Cancel()
	_ = owner.Wait()
	os.Exit(0)
}

func TestOwnedOutputDiesWithCrashedParentWhileStdoutStaysBlocked(t *testing.T) {
	dataRead, dataWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer dataRead.Close()
	deathRead, deathWrite, err := os.Pipe()
	if err != nil {
		dataWrite.Close()
		t.Fatal(err)
	}
	defer deathRead.Close()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	parent := exec.Command(executable, "-test.run=^TestBridgeOutputParentFixture$", "--", "bridge-parent-fixture")
	parent.Stdout = dataWrite
	parent.ExtraFiles = []*os.File{deathWrite}
	if err := parent.Start(); err != nil {
		dataWrite.Close()
		deathWrite.Close()
		t.Fatal(err)
	}
	dataWrite.Close()
	deathWrite.Close()
	defer parent.Wait()
	defer parent.Process.Kill()
	// Read one byte only. The output remains blocked; draining its data pipe
	// would hide the orphan by allowing a successful write followed by EOF.
	if _, err := io.ReadFull(dataRead, make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	if err := parent.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := deathRead.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := deathRead.Read(make([]byte, 1)); err != io.EOF {
		t.Fatal("the blocked output child survived its actual parent's death", err)
	}
}

func TestOwnedOutputCancelsBlockedChildPrivatePipe(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	copyFD, err := unix.Dup(int(writer.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(copyFD)
	before, err := unix.FcntlInt(uintptr(copyFD), unix.F_GETFL, 0)
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	owner, err := startBridgeOutput(executable, []string{"-test.run=^TestBridgeOutputProcessHelper$", "--", "bridge-output-fixture"}, writer)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Wait()
	defer owner.Cancel()
	done := make(chan error, 1)
	go func() {
		_, err := owner.Write(bytes.Repeat([]byte{'x'}, proto.MaxFrame-1024))
		done <- err
	}()
	// Receiving actual bytes proves the owned helper reached the raw pipe.
	if _, err := io.ReadFull(reader, make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	_ = owner.Cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a blocked raw write was reported successful")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("owned cancellation left the blocked writer alive")
	}
	joined := make(chan struct{})
	go func() { _ = owner.Wait(); close(joined) }()
	select {
	case <-joined:
	case <-time.After(3 * time.Second):
		t.Fatal("the actual output child was not joined")
	}
	after, err := unix.FcntlInt(uintptr(copyFD), unix.F_GETFL, 0)
	if err != nil || before != after {
		t.Fatal("output cancellation changed the inherited pipe's OFD flags", err)
	}
}

func TestOwnedOutputStartFailureClosesTransferredFile(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if _, err := NewProcessBridgeOutput(filepath.Join(t.TempDir(), "missing-helper"), writer); err == nil {
		t.Fatal("the missing output helper started")
	}
	if _, err := writer.Write([]byte("x")); err == nil {
		t.Fatal("the explicitly transferred output leaked after start failure")
	}
}
