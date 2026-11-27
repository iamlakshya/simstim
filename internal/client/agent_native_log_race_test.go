//go:build darwin || linux

package client

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAgentNativeLogSyncCannotMissBytesBetweenRawReadAndCommit(t *testing.T) {
	nativeLogTestEnv(t)
	log := nativeTestLog(t, t.Context())
	if err := log.AttachOwner(func(error) {}); err != nil {
		t.Fatal(err)
	}
	entered, resume := make(chan struct{}), make(chan struct{})
	var observed, released sync.Once
	release := func() { released.Do(func() { close(resume) }) }
	t.Cleanup(release)
	log.mu.Lock()
	log.afterRead = func() { observed.Do(func() { close(entered) }); <-resume }
	log.mu.Unlock()
	writer := nativeTestWriter(t, log)
	const want = "debug1: already written before the native ACK\r\n"
	if _, err := io.WriteString(writer, want); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("the FIFO bytes were not read")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- log.Sync(ctx) }()
	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Sync accepted a prefix while raw bytes lacked their counter/commit: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Sync ignored cancellation while the atomic read step was held")
	}
	release()
	log.mu.Lock()
	log.afterRead = nil
	log.mu.Unlock()
	if err := log.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	var got []byte
	if err := log.ScanSnapshot(t.Context(), func(reader io.Reader) error { var err error; got, err = io.ReadAll(reader); return err }); err != nil || string(got) != want {
		t.Fatalf("committed prefix lost raw FIFO bytes: %q, %v", got, err)
	}
}

func TestAgentNativeLogRemembersCancellationBeforeOwnerAssignment(t *testing.T) {
	nativeLogTestEnv(t)
	ctx, cancel := context.WithCancel(t.Context())
	log := nativeTestLog(t, ctx)
	cancel()
	waitNativeLog(t, func() bool { return log.Failure() != nil })
	var calls atomic.Int32
	err := log.AttachOwner(func(reason error) {
		if !errors.Is(reason, context.Canceled) || !errors.Is(log.Failure(), context.Canceled) {
			t.Error("early cancellation was lost before owner callback")
		}
		calls.Add(1)
	})
	if !errors.Is(err, context.Canceled) || calls.Load() != 1 {
		t.Fatalf("late owner was not cancelled exactly once: %v, calls=%d", err, calls.Load())
	}
	_ = log.Close()
	if calls.Load() != 1 {
		t.Fatal("closing a failed log cancelled its owner twice")
	}
}

func TestAgentNativeLogOwnerOutlivesTheDialContext(t *testing.T) {
	nativeLogTestEnv(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	log := nativeTestLog(t, ctx)
	var calls atomic.Int32
	if err := log.AttachOwner(func(error) { calls.Add(1) }); err != nil {
		t.Fatal(err)
	}
	cancel()
	writer := nativeTestWriter(t, log)
	if _, err := io.WriteString(writer, "a live owned SSH process\n"); err != nil {
		t.Fatal(err)
	}
	if err := log.Sync(t.Context()); err != nil || log.Failure() != nil || calls.Load() != 0 {
		t.Fatalf("dial context ended the owned log lifetime: %v failure=%v calls=%d", err, log.Failure(), calls.Load())
	}
}

func TestAgentNativeLogRejectsASecondOwnerAndCancelsOnlyAssignedCallbacks(t *testing.T) {
	nativeLogTestEnv(t)
	log := nativeTestLog(t, t.Context())
	var first, second atomic.Int32
	if err := log.AttachOwner(func(error) { first.Add(1) }); err != nil {
		t.Fatal(err)
	}
	if err := log.AttachOwner(func(error) { second.Add(1) }); err == nil || first.Load() != 1 || second.Load() != 1 {
		t.Fatalf("private log was shared by owners: %v first=%d second=%d", err, first.Load(), second.Load())
	}
}

func TestAgentNativeLogSnapshotRejectsPathAndDirectoryReplacement(t *testing.T) {
	for _, replacement := range []string{"transcript", "fifo", "directory"} {
		t.Run(replacement, func(t *testing.T) {
			nativeLogTestEnv(t)
			log := nativeTestLog(t, t.Context())
			if err := log.AttachOwner(func(error) {}); err != nil {
				t.Fatal(err)
			}
			writer := nativeTestWriter(t, log)
			if _, err := io.WriteString(writer, "an original complete prefix\n"); err != nil {
				t.Fatal(err)
			}
			if err := log.Sync(t.Context()); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Dir(log.Path())
			path := filepath.Join(dir, "transcript.log")
			if replacement == "fifo" {
				path = log.Path()
			} else if replacement == "directory" {
				if err := os.Rename(dir, dir+"-moved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				path = filepath.Join(dir, "keep")
			}
			if replacement != "directory" {
				if err := os.Rename(path, path+"-moved"); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(path, []byte("foreign"), 0o600); err != nil {
				t.Fatal(err)
			}
			called := false
			if err := log.ScanSnapshot(t.Context(), func(io.Reader) error { called = true; return nil }); err == nil || called {
				t.Fatal("a replaced private path reached the metadata verifier")
			}
			_ = log.Close()
			if data, err := os.ReadFile(path); err != nil || string(data) != "foreign" {
				t.Fatalf("cleanup removed a foreign replacement: %q, %v", data, err)
			}
		})
	}
}

func TestAgentNativeLogSnapshotCancellationDoesNotHoldThePumpMutex(t *testing.T) {
	nativeLogTestEnv(t)
	log := nativeTestLog(t, t.Context())
	if err := log.AttachOwner(func(error) {}); err != nil {
		t.Fatal(err)
	}
	writer := nativeTestWriter(t, log)
	if _, err := io.WriteString(writer, "complete prefix\n"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	err := log.ScanSnapshot(ctx, func(reader io.Reader) error {
		cancel()
		_, err := io.Copy(io.Discard, reader)
		return err
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("snapshot cancellation changed its kind: %v", err)
	}
	if _, err := io.WriteString(writer, "still pumping\n"); err != nil {
		t.Fatal(err)
	}
	if err := log.Sync(t.Context()); err != nil {
		t.Fatalf("a cancelled scan retained the pump mutex or descriptor: %v", err)
	}
}

func TestAgentNativeLogRawDescriptorCannotCloseOrReuseDuringControl(t *testing.T) {
	root := nativeLogTestEnv(t)
	log := nativeTestLog(t, t.Context())
	entered, release := make(chan struct{}), make(chan struct{})
	var resumed sync.Once
	resume := func() { resumed.Do(func() { close(release) }) }
	t.Cleanup(resume)
	controlDone := make(chan struct{})
	go func() {
		_ = log.raw.Control(func(uintptr) { close(entered); <-release })
		close(controlDone)
	}()
	<-entered
	closed := make(chan error, 1)
	go func() { closed <- log.Close() }()
	waitNativeLog(t, func() bool { return log.closed.Load() })
	select {
	case err := <-closed:
		t.Fatalf("Close bypassed the held raw descriptor reference: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	resume()
	select {
	case <-controlDone:
	case <-time.After(time.Second):
		t.Fatal("raw descriptor callback did not finish")
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("private log did not join after the descriptor reference ended")
	}
	foreign, err := os.OpenFile(filepath.Join(root, "foreign"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer foreign.Close()
	_ = log.Close()
	if _, err := foreign.WriteString("keep"); err != nil {
		t.Fatalf("repeated Close affected a replacement descriptor: %v", err)
	}
}

func TestAgentNativeLogReadFailureCannotBecomeAValidOldPrefix(t *testing.T) {
	nativeLogTestEnv(t)
	log := nativeTestLog(t, t.Context())
	if err := log.AttachOwner(func(error) {}); err != nil {
		t.Fatal(err)
	}
	writer := nativeTestWriter(t, log)
	if _, err := io.WriteString(writer, "valid earlier prefix\n"); err != nil {
		t.Fatal(err)
	}
	if err := log.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	_ = log.fifo.Close()
	waitNativeLog(t, func() bool { return log.Failure() != nil })
	var output bytes.Buffer
	err := log.ScanSnapshot(t.Context(), func(reader io.Reader) error { _, err := io.Copy(&output, reader); return err })
	if err == nil || output.Len() != 0 || strings.Contains(err.Error(), "valid earlier") {
		t.Fatalf("a failed reader returned an old prefix as proof: err=%v output=%d", err, output.Len())
	}
}

func TestAgentNativeLogCloseIsBoundedWithTheCommitMutexBlocked(t *testing.T) {
	nativeLogTestEnv(t)
	log := nativeTestLog(t, t.Context())
	var callbacks atomic.Int32
	if err := log.AttachOwner(func(error) { callbacks.Add(1); _ = log.Failure() }); err != nil {
		t.Fatal(err)
	}
	entered, resume := make(chan struct{}), make(chan struct{})
	var observed, released sync.Once
	release := func() { released.Do(func() { close(resume) }) }
	t.Cleanup(release)
	log.mu.Lock()
	log.afterRead = func() { observed.Do(func() { close(entered) }); <-resume }
	log.mu.Unlock()
	writer := nativeTestWriter(t, log)
	if _, err := io.WriteString(writer, "held before regular commit\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("the commit section was not entered")
	}
	closed := make(chan error, 1)
	go func() { closed <- log.Close() }()
	select {
	case err := <-closed:
		if err == nil || log.Failure() == nil || callbacks.Load() != 1 {
			t.Fatalf("blocked shutdown did not fail and cancel independently: %v failure=%v callbacks=%d", err, log.Failure(), callbacks.Load())
		}
	case <-time.After(sshWaitDelay + time.Second):
		t.Fatal("Close waited for the pump mutex before its shutdown deadline")
	}
	select {
	case <-log.inputDone:
	case <-time.After(time.Second):
		t.Fatal("the FIFO was not interrupted while the commit mutex was held")
	}
	release()
	select {
	case <-log.cleanupDone:
	case <-time.After(time.Second):
		t.Fatal("deferred cleanup did not finish after the commit section resumed")
	}
}

func TestAgentNativeLogFinishIsBoundedWithTheCommitMutexBlocked(t *testing.T) {
	nativeLogTestEnv(t)
	log := nativeTestLog(t, t.Context())
	var callbacks atomic.Int32
	if err := log.AttachOwner(func(error) { callbacks.Add(1) }); err != nil {
		t.Fatal(err)
	}
	entered, resume := make(chan struct{}), make(chan struct{})
	var observed, released sync.Once
	release := func() { released.Do(func() { close(resume) }) }
	t.Cleanup(release)
	log.mu.Lock()
	log.afterRead = func() { observed.Do(func() { close(entered) }); <-resume }
	log.mu.Unlock()
	writer := nativeTestWriter(t, log)
	if _, err := io.WriteString(writer, "held before final classification\n"); err != nil {
		t.Fatal(err)
	}
	<-entered
	finished := make(chan error, 1)
	go func() {
		_, err := log.FinishScan(func(io.Reader) error { t.Error("blocked capture reached final classifier"); return nil })
		finished <- err
	}()
	select {
	case err := <-finished:
		if err == nil || callbacks.Load() != 1 {
			t.Fatalf("blocked Finish lost its failure: %v callbacks=%d", err, callbacks.Load())
		}
	case <-time.After(2*sshWaitDelay + time.Second):
		t.Fatal("Finish exceeded its overall drain, scan and shutdown budget")
	}
	release()
	select {
	case <-log.cleanupDone:
	case <-time.After(time.Second):
		t.Fatal("Finish did not preserve deferred owned cleanup")
	}
}
