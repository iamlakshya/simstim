package client

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"
)

type trustContextResult struct {
	warn *UserError
	err  error
}

// This per-operation context observes the public cancellation wait. It does
// not install a global hook or depend on scheduler sleeps.
type observedTrustWaitContext struct {
	context.Context
	observed chan struct{}
	once     sync.Once
}

func (c *observedTrustWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.observed) })
	return c.Context.Done()
}
func waitForTrustCancellationWait(t *testing.T, waiting <-chan struct{}) {
	t.Helper()
	select {
	case <-waiting:
	case <-time.After(trustWait):
		t.Fatal("operation never observed cancellation while its lock was held")
	}
}

func TestSetupTrustContextCancellationWhileFileLockWaits(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if _, err := RememberSetup("box", "/old", true, "old-hash"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(trustPath())
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := lockPath(trustPath()+".lock", trustWait, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { unlock() }()
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &observedTrustWaitContext{Context: parent, observed: make(chan struct{})}
	done := make(chan trustContextResult, 1)
	go func() {
		w, e := RememberSetupContext(ctx, "box", "/new", true, "new-hash")
		done <- trustContextResult{w, e}
	}()
	received := false
	defer func() {
		cancel()
		unlock()
		unlock = func() {}
		if !received {
			<-done
		}
	}()
	waitForTrustCancellationWait(t, ctx.observed)
	cancel()
	select {
	case result := <-done:
		received = true
		if result.warn != nil || !errors.Is(result.err, context.Canceled) {
			t.Fatalf("cancel result=%+v", result)
		}
	case <-time.After(trustWait):
		t.Fatal("cancel did not interrupt the existing bounded lock wait")
	}
	after, err := os.ReadFile(trustPath())
	if err != nil || string(before) != string(after) {
		t.Fatalf("cancelled answer changed the file: %v", err)
	}
	if _, err := os.Stat(trustPath() + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("uncommitted temp survives: %v", err)
	}
	unlock()
	unlock = func() {}
	if _, err := RememberSetupContext(context.Background(), "box", "/new", true, "new-hash"); err != nil {
		t.Fatal(err)
	}
	got := LoadSetupAnswers("box")
	if got.Hashes["/old"] != "old-hash" || got.Hashes["/new"] != "new-hash" {
		t.Fatalf("serialized hash answers=%+v", got)
	}
}

func TestSetupTrustContextCancellationWhileProcessLockWaits(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	trustMu.Lock()
	locked := true
	defer func() {
		if locked {
			trustMu.Unlock()
		}
	}()
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &observedTrustWaitContext{Context: parent, observed: make(chan struct{})}
	invoked := make(chan struct{})
	done := make(chan trustContextResult, 1)
	go func() {
		close(invoked)
		w, e := RememberSetupContext(ctx, "box", "/new", true, "hash")
		done <- trustContextResult{w, e}
	}()
	received := false
	defer func() {
		cancel()
		if locked {
			trustMu.Unlock()
			locked = false
		}
		if !received {
			<-done
		}
	}()
	<-invoked
	waitForTrustCancellationWait(t, ctx.observed)
	cancel()
	select {
	case result := <-done:
		received = true
		if result.warn != nil || !errors.Is(result.err, context.Canceled) {
			t.Fatalf("cancel result=%+v", result)
		}
	case <-time.After(trustWait):
		t.Fatal("cancel could not leave the process-lock wait")
	}
	trustMu.Unlock()
	locked = false
	if _, err := os.Stat(StateDir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancel created state: %v", err)
	}
}

func TestSetupTrustContextPreCancelledDoesNotRecoverCorruptFile(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	writeTrust(t, "{not-json")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w, err := RememberSetupContext(ctx, "box", "/r", true, "hash")
	if w != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel result=%v,%v", w, err)
	}
	data, err := os.ReadFile(trustPath())
	if err != nil || string(data) != "{not-json" {
		t.Fatalf("corrupt source changed: %q,%v", data, err)
	}
	if _, err := os.Stat(trustPath() + ".bad"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancel recovered a file: %v", err)
	}
}

func TestSetupTrustCommittedAnswerSurvivesLaterCancellation(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	w, err := RememberSetupContext(ctx, "box", "/r", true, "committed-hash")
	if w != nil || err != nil {
		t.Fatalf("commit=%v,%v", w, err)
	}
	before, err := os.ReadFile(trustPath())
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	after, err := os.ReadFile(trustPath())
	if err != nil || string(before) != string(after) {
		t.Fatalf("later cancel rolled back commit: %v", err)
	}
	if got := LoadSetupAnswers("box"); got.Hashes["/r"] != "committed-hash" {
		t.Fatalf("commit lost hash: %+v", got)
	}
}

func TestSetupTrustContextStopsBeforePersistence(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if _, err := RememberSetup("box", "/old", true, "old-hash"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(trustPath())
	if err != nil {
		t.Fatal(err)
	}
	trustMu.Lock()
	defer trustMu.Unlock()
	unlock, err := lockTrust()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	file, warn, stuck := loadTrust()
	if warn != nil || stuck {
		t.Fatalf("fixture trust=%v,%v", warn, stuck)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := saveAnswerContext(ctx, file, "box", "/new", true, "new-hash"); !errors.Is(err, context.Canceled) {
		t.Fatalf("persistence ignored cancel: %v", err)
	}
	after, err := os.ReadFile(trustPath())
	if err != nil || string(before) != string(after) {
		t.Fatalf("cancel changed committed trust: %v", err)
	}
	if _, err := os.Stat(trustPath() + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancel wrote temporary state: %v", err)
	}
}
