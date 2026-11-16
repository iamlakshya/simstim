//go:build linux || darwin

package host

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
)

func TestOwnedInputObservationFailureStopsActualBlockedReaderBeforeWait(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	ready := make(chan struct{})
	defer func() {
		select {
		case <-ready:
		default:
			close(ready)
		}
	}()
	observationErr := syscall.EIO
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	owner, err := startBridgeIOObserved(executable, []string{"-test.run=^TestBridgeInputProcessHelper$", "--", "bridge-input-fixture"}, reader, true,
		func(int) (syscall.WaitStatus, bool, error) { <-ready; return 0, false, observationErr })
	if err != nil {
		t.Fatal(err)
	}
	input := &processBridgeInput{processBridgeIO: owner, r: bufio.NewReader(owner.conn)}
	t.Cleanup(func() { _ = input.Cancel(); _ = input.Wait() })
	if _, err := writer.Write([]byte{'x'}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(input, make([]byte, 1)); err != nil {
		t.Fatal("the actual child never read the inherited input pipe", err)
	}
	blocked := make(chan error, 1)
	go func() { _, err := input.Read(make([]byte, 1)); blocked <- err }()
	close(ready) // Observation now fails while the raw input stays open.
	select {
	case err := <-blocked:
		if err == nil {
			t.Fatal("uncertain child exit left input admissible")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("uncertain observation did not revoke the actual input reader")
	}
	assertBridgeObservationWaitV1(t, owner, observationErr)
}

func TestOwnedOutputObservationFailureStopsActualBlockedWriterBeforeWait(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	ready := make(chan struct{})
	defer func() {
		select {
		case <-ready:
		default:
			close(ready)
		}
	}()
	observationErr := syscall.EIO
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	owner, err := startBridgeIOObserved(executable, []string{"-test.run=^TestBridgeOutputProcessHelper$", "--", "bridge-output-fixture"}, writer, false,
		func(int) (syscall.WaitStatus, bool, error) { <-ready; return 0, false, observationErr })
	if err != nil {
		t.Fatal(err)
	}
	output := &processBridgeOutput{processBridgeIO: owner, w: proto.NewWriter(owner.conn), r: bufio.NewReader(owner.conn)}
	t.Cleanup(func() { _ = output.Cancel(); _ = output.Wait() })
	written := make(chan error, 1)
	go func() { _, err := output.Write(bytes.Repeat([]byte{'x'}, proto.MaxFrame-1024)); written <- err }()
	if _, err := io.ReadFull(reader, make([]byte, 1)); err != nil {
		t.Fatal("the actual child never wrote the inherited output pipe", err)
	}
	close(ready) // The remaining raw output cannot drain through this reader.
	select {
	case err := <-written:
		if err == nil {
			t.Fatal("uncertain exit reported the blocked raw write as complete")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("uncertain observation did not revoke the actual output writer")
	}
	assertBridgeObservationWaitV1(t, owner, observationErr)
}

func assertBridgeObservationWaitV1(t *testing.T, owner *processBridgeIO, cause error) {
	t.Helper()
	joined := make(chan error, 1)
	go func() { joined <- owner.Wait() }()
	select {
	case err := <-joined:
		if !errors.Is(err, cause) {
			t.Fatal("actual join lost the original native observation error", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("uncertainty revoked the signal right before stopping the real child")
	}
	owner.mu.Lock()
	gone, state := owner.gone, owner.cmd.ProcessState
	owner.mu.Unlock()
	if !gone || state == nil {
		t.Fatal("Wait returned without the actual child's native process state")
	}
	_ = owner.Cancel()
}
