//go:build linux || darwin

package host

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
	"golang.org/x/sys/unix"
)

func TestBridgeInputProcessHelper(t *testing.T) {
	if len(os.Args) == 0 || os.Args[len(os.Args)-1] != "bridge-input-fixture" {
		return
	}
	if os.Getenv("BRIDGE_INPUT_TEST_SECRET") != "" {
		os.Exit(2)
	}
	if err := RunProcessBridgeInput(); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func bridgeInputFixtureOwner(t *testing.T, file *os.File, extra ...*os.File) OwnedBridgeInput {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	owner, err := startBridgeInput(executable, []string{"-test.run=^TestBridgeInputProcessHelper$", "--", "bridge-input-fixture"}, file, extra...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Cancel(); _ = owner.Wait() })
	return owner
}

func TestOwnedInputWaitBeforeReadPreservesAllFinalBytes(t *testing.T) {
	t.Setenv("BRIDGE_INPUT_TEST_SECRET", "must-not-reach-child")
	wanted := bytes.Repeat([]byte{'a', 0, '\r', '\n', '\x1b', '[', '3', '1', 'm'}, 200)
	file, err := os.CreateTemp(t.TempDir(), "raw-input")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(wanted); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	owner := bridgeInputFixtureOwner(t, file)
	// The socket can hold this one small final frame. Waiting for the real child
	// first must not close the parent socket and discard its final frames.
	if err := owner.Wait(); err != nil {
		t.Fatal(err)
	}
	var got bytes.Buffer
	buffer := make([]byte, 7)
	for {
		n, err := owner.Read(buffer)
		got.Write(buffer[:n])
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(got.Bytes(), wanted) {
		t.Fatalf("input changed or its final frames were lost: got=%d want=%d", got.Len(), len(wanted))
	}
}

func TestOwnedInputCancellationJoinsBlockedRawReaderWithoutFlagChanges(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	copyFD, err := unix.Dup(int(reader.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(copyFD)
	before, err := unix.FcntlInt(uintptr(copyFD), unix.F_GETFL, 0)
	if err != nil {
		t.Fatal(err)
	}
	owner := bridgeInputFixtureOwner(t, reader)
	if _, err := writer.Write([]byte{'x'}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(owner, make([]byte, 1)); err != nil {
		t.Fatal("the actual input helper never read its inherited pipe", err)
	}
	blocked := make(chan error, 1)
	go func() { _, err := owner.Read(make([]byte, 1)); blocked <- err }()
	_ = owner.Cancel()
	select {
	case err := <-blocked:
		if err == nil {
			t.Fatal("cancelled input supplied a new byte")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("owned input cancellation left a blocked Read alive")
	}
	joined := make(chan error, 1)
	go func() { joined <- owner.Wait() }()
	select {
	case err := <-joined:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the actual input child was not joined")
	}
	after, err := unix.FcntlInt(uintptr(copyFD), unix.F_GETFL, 0)
	if err != nil || before != after {
		t.Fatal("owned cancellation changed inherited input OFD flags", err)
	}
}

func TestOwnedInputStartFailureClosesTransferredFile(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := NewProcessBridgeInput(filepath.Join(t.TempDir(), "missing-helper"), reader); err == nil {
		t.Fatal("the missing input helper started")
	}
	if _, err := reader.Read(make([]byte, 1)); err == nil {
		t.Fatal("a transferred input leaked after start failure")
	}
}

func TestBridgeInputCoreKeepsBoundedExactChunksIncludingFinalEOFData(t *testing.T) {
	wanted := bytes.Repeat([]byte{0, 'a', '\x1b', '\r', '\n'}, 2000)
	var framed bytes.Buffer
	if err := runBridgeInput(&inputFinalEOFReader{data: wanted}, &framed); err != nil {
		t.Fatal(err)
	}
	var got bytes.Buffer
	reader := bufio.NewReader(&framed)
	for {
		frame, err := proto.ReadLimit(reader, 4096+1)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || frame.Type != proto.TInput || len(frame.Payload) == 0 || len(frame.Payload) > 4096 {
			t.Fatalf("invalid private input chunk: %+v %v", frame, err)
		}
		got.Write(frame.Payload)
	}
	if !bytes.Equal(got.Bytes(), wanted) {
		t.Fatal("the input helper transformed bytes or lost data returned with EOF")
	}
}

type inputFinalEOFReader struct{ data []byte }

func (r *inputFinalEOFReader) Read(p []byte) (int, error) {
	n := copy(p, r.data)
	r.data = r.data[n:]
	if len(r.data) == 0 {
		return n, io.EOF
	}
	return n, nil
}

func TestBridgeInputParentFixture(t *testing.T) {
	if len(os.Args) == 0 || os.Args[len(os.Args)-1] != "bridge-input-parent-fixture" {
		return
	}
	executable, err := os.Executable()
	if err != nil {
		os.Exit(2)
	}
	death := os.NewFile(3, "input-child-lifetime")
	owner, err := startBridgeInput(executable, []string{"-test.run=^TestBridgeInputProcessHelper$", "--", "bridge-input-fixture"}, os.Stdin, death)
	death.Close()
	if err != nil {
		os.Exit(2)
	}
	if _, err := io.ReadFull(owner, make([]byte, 1)); err != nil {
		os.Exit(2)
	}
	if _, err := os.Stdout.Write([]byte{'r'}); err != nil {
		os.Exit(2)
	}
	_, _ = owner.Read(make([]byte, 1))
	_ = owner.Cancel()
	_ = owner.Wait()
	os.Exit(0)
}

func TestOwnedInputDiesWithCrashedParentWhileStdinStaysOpen(t *testing.T) {
	inputRead, inputWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer inputWrite.Close()
	readyRead, readyWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readyRead.Close()
	deathRead, deathWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer deathRead.Close()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	parent := exec.Command(executable, "-test.run=^TestBridgeInputParentFixture$", "--", "bridge-input-parent-fixture")
	parent.Stdin, parent.Stdout = inputRead, readyWrite
	parent.ExtraFiles = []*os.File{deathWrite}
	if err := parent.Start(); err != nil {
		t.Fatal(err)
	}
	inputRead.Close()
	readyWrite.Close()
	deathWrite.Close()
	defer parent.Wait()
	defer parent.Process.Kill()
	if _, err := inputWrite.Write([]byte{'x'}); err != nil {
		t.Fatal(err)
	}
	if err := readyRead.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(readyRead, make([]byte, 1)); err != nil {
		t.Fatal("the original input child never read the actual pipe", err)
	}
	if err := parent.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := deathRead.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := deathRead.Read(make([]byte, 1)); err != io.EOF {
		t.Fatal("the blocked input child survived its actual parent's death", err)
	}
}
