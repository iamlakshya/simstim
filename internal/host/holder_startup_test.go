package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/testhome"
)

func startupTestManifest(t *testing.T) *Manifest {
	t.Helper()
	root := t.TempDir()
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_CONFIG_DIRS", "XDG_DATA_DIRS"} {
		dir := filepath.Join(root, key)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv(key, dir)
	}
	runtime, err := testhome.ShortTempDir("pending")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtime) })
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	t.Setenv(holderLockFDEnv, "")
	t.Setenv(StartStallEnv, "")
	m := &Manifest{Schema: 1, ID: NewID(), Name: "pending", Agent: AgentInfo{Kind: "custom", Argv: []string{"/unavailable-agent-must-never-start"}},
		Cwd: root, Env: []string{"PATH=/usr/bin:/bin"}, Term: TermInfo{Cols: 80, Rows: 24}, Status: Status{State: StateStarting}}
	if err := paths.EnsurePrivateDir(paths.SessionDir(m.ID)); err != nil {
		t.Fatal(err)
	}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	return m
}

func awaitStartupTest[T any](t *testing.T, done <-chan T) T {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(5 * time.Second):
		t.Fatal("pending startup did not finish its owned work")
		var zero T
		return zero
	}
}

type startupTestWorker[T any] struct {
	done   chan struct{}
	result T
}

func ownedStartupWorker[T any](t *testing.T, work func() T, stop func()) *startupTestWorker[T] {
	t.Helper()
	w := &startupTestWorker[T]{done: make(chan struct{})}
	go func() { w.result = work(); close(w.done) }()
	t.Cleanup(func() {
		select {
		case <-w.done:
		default:
			stop()
		}
		w.wait(t)
	})
	return w
}

func (w *startupTestWorker[T]) wait(t *testing.T) T {
	t.Helper()
	awaitStartupTest(t, w.done)
	return w.result
}

func ownedRunHolder(t *testing.T, id string, unblock func()) *startupTestWorker[error] {
	t.Helper()
	return ownedStartupWorker(t, func() error { return RunHolder(id) }, func() {
		_, _ = HolderRequest(id, cancelPendingStartOp, nil, 100*time.Millisecond)
		unblock()
	})
}

func assertPendingStopReleased(t *testing.T, id string) {
	t.Helper()
	if holderLockHeld(id) {
		t.Fatal("successful pending acknowledgement preceded holder.lock release")
	}
	for _, path := range []string{paths.Socket(id), paths.HookSocket(id)} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("successful pending acknowledgement left endpoint %s: %v", path, err)
		}
	}
}

func TestPendingControlStopsBeforeManifestLoadWithoutLateSave(t *testing.T) {
	m := startupTestManifest(t)
	before, err := os.ReadFile(paths.Manifest(m.ID))
	if err != nil {
		t.Fatal(err)
	}
	oldLoad := loadStartingManifest
	t.Cleanup(func() { loadStartingManifest = oldLoad })
	entered, unblock := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(unblock) })
	loadStartingManifest = func(ctx context.Context, id string) (*Manifest, error) {
		close(entered)
		select {
		case <-ctx.Done():
		case <-unblock:
		}
		return oldLoad(ctx, id)
	}
	done := ownedRunHolder(t, m.ID, release)
	awaitStartupTest(t, entered)
	if !holderLockHeld(m.ID) {
		t.Fatal("delayed manifest load lost its holder lock")
	}
	if _, err := HolderRequest(m.ID, "sockets", nil, time.Second); err == nil {
		t.Fatal("an early endpoint claimed readiness before manifest loading")
	}
	if _, err := HolderRequest(m.ID, "kill", nil, 2*time.Second); err != nil {
		t.Fatalf("early pending stop failed: %v", err)
	}
	assertPendingStopReleased(t, m.ID)
	if err := done.wait(t); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled manifest load = %v", err)
	}
	after, err := os.ReadFile(paths.Manifest(m.ID))
	if err != nil || string(after) != string(before) {
		t.Fatalf("pending stop changed the manifest: %v", err)
	}
	if err := os.RemoveAll(paths.SessionDir(m.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(paths.SessionDir(m.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("joined startup recreated the session folder: %v", err)
	}
}

func TestPendingControlCancelsTheActualAfterLoadBoundary(t *testing.T) {
	m := startupTestManifest(t)
	oldAfter := afterHolderLoad
	t.Cleanup(func() { afterHolderLoad = oldAfter })
	entered, unblock := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(unblock) })
	afterHolderLoad = func(ctx context.Context, _ string) {
		close(entered)
		select {
		case <-ctx.Done():
		case <-unblock:
		}
	}
	done := ownedRunHolder(t, m.ID, release)
	awaitStartupTest(t, entered)
	if _, err := HolderRequest(m.ID, "kill", nil, 2*time.Second); err != nil {
		t.Fatalf("post-load pending stop failed: %v", err)
	}
	assertPendingStopReleased(t, m.ID)
	if err := done.wait(t); err != nil {
		t.Fatalf("cancelled post-load start = %v", err)
	}
}

func TestPendingControlCannotAcknowledgeUnjoinedStartup(t *testing.T) {
	m := startupTestManifest(t)
	oldAfter := afterHolderLoad
	t.Cleanup(func() { afterHolderLoad = oldAfter })
	entered, unblock := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(unblock) })
	t.Cleanup(release)
	afterHolderLoad = func(context.Context, string) { close(entered); <-unblock }
	done := ownedRunHolder(t, m.ID, release)
	awaitStartupTest(t, entered)
	if _, err := HolderRequest(m.ID, "kill", nil, 100*time.Millisecond); err == nil {
		t.Fatal("stuck synchronous startup received a successful stop acknowledgement")
	}
	if !holderLockHeld(m.ID) {
		t.Fatal("stuck synchronous startup released its ownership before joining")
	}
	select {
	case <-done.done:
		t.Fatalf("an unjoined startup owner returned: %v", done.result)
	default:
	}
	release()
	if err := done.wait(t); err != nil {
		t.Fatal(err)
	}
	assertPendingStopReleased(t, m.ID)
}

func startupControlFixture(t *testing.T) (*Manifest, *holderStartup, func()) {
	return startupControlFixtureWithPeerCheck(t, false, nil)
}

func startupControlFixtureWithPeerCheck(t *testing.T, peerCheck bool, beforeStart func(*Manifest)) (*Manifest, *holderStartup, func()) {
	t.Helper()
	m := startupTestManifest(t)
	if beforeStart != nil {
		beforeStart(m)
	}
	release, err := acquireHolderLock(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	p, err := beginHolderStartupWithPeerCheck(m.ID, peerCheck)
	if err != nil {
		release()
		t.Fatal(err)
	}
	finish := sync.OnceFunc(func() { p.finish(release) })
	t.Cleanup(finish)
	return m, p, finish
}

func TestPendingControlRejectsStopAfterAgentLaunchEntered(t *testing.T) {
	m, p, _ := startupControlFixture(t)
	if err := p.beginAgent(); err != nil {
		t.Fatal(err)
	}
	_, err := HolderRequest(m.ID, "kill", nil, time.Second)
	var refusal *proto.Error
	if !errors.As(err, &refusal) || refusal.Code != "starting" || refusal.Hint == "" || p.ctx.Err() != nil {
		t.Fatalf("entered launch claimed a weak pending stop: %v, cancellation %v", err, p.ctx.Err())
	}
	if !holderLockHeld(m.ID) {
		t.Fatal("post-launch refusal released the actual owner")
	}
}

func TestPendingControlSecretsNeedLoadedManifestAndStopBeforeLaunch(t *testing.T) {
	m, p, finish := startupControlFixture(t)
	values := secretsMsg{Env: map[string]api.Secret{"EXPECTED": "fixture-value", "EXTRA": "unused-value"}}
	if _, err := HolderRequest(m.ID, "secrets", values, time.Second); err == nil {
		t.Fatal("pending control accepted names before loading the manifest")
	}
	m.Secrets = []string{"EXPECTED"}
	h := &Holder{m: m}
	p.prepare(h, false)
	secrets := ownedStartupWorker(t, func() []string { return p.awaitSecrets(m.Secrets, time.Second) }, p.cancel)
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := HolderRequest(m.ID, "sockets", nil, 100*time.Millisecond); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("validated secrets receiver never became ready")
		}
	}
	if _, err := HolderRequest(m.ID, "secrets", values, time.Second); err != nil {
		t.Fatal(err)
	}
	if got := secrets.wait(t); strings.Join(got, "\n") != "EXPECTED=fixture-value" {
		t.Fatalf("secrets selection = %q", got)
	}
	stop := ownedStartupWorker(t, func() error {
		_, err := HolderRequest(m.ID, "kill", nil, 2*time.Second)
		return err
	}, finish)
	awaitStartupTest(t, p.ctx.Done())
	if err := p.beginAgent(); !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled pending owner could enter agent launch: %v", err)
	}
	finish()
	if err := stop.wait(t); err != nil {
		t.Fatal(err)
	}
	assertPendingStopReleased(t, m.ID)
}

func TestPendingControlRestrictedPeerCannotStopOrSupplySecrets(t *testing.T) {
	m, p, _ := startupControlFixtureWithPeerCheck(t, true, func(m *Manifest) {
		fakePeer(t, otherCgroup(m.ID), nil)
	})
	for _, op := range []string{"kill", cancelPendingStartOp, "secrets"} {
		_, err := HolderRequest(m.ID, op, secretsMsg{Env: map[string]api.Secret{"EXPECTED": "fixture-value"}}, time.Second)
		var refusal *proto.Error
		if !errors.As(err, &refusal) || refusal.Code != errInside.Code || p.ctx.Err() != nil {
			t.Fatalf("restricted %s = %v, cancellation %v", op, err, p.ctx.Err())
		}
	}
}

func TestPendingControlRenameKeepsTheCanonicalSavedName(t *testing.T) {
	m, p, _ := startupControlFixture(t)
	if _, err := HolderRequest(m.ID, "rename", map[string]string{"name": "renamed"}, time.Second); err != nil {
		t.Fatal(err)
	}
	disk, err := LoadManifest(m.ID)
	if err != nil || disk.Name != "renamed" || disk.Status.State != StateStarting || disk.Run.AgentPID != 0 {
		t.Fatalf("acknowledged pending rename = %+v, %v", disk, err)
	}
	if err := p.beginAgent(); err != nil {
		t.Fatal(err)
	}
	if _, err := HolderRequest(m.ID, "rename", map[string]string{"name": "late"}, time.Second); err == nil {
		t.Fatal("pending rename could write after entering the actual launch owner")
	}
	disk, err = LoadManifest(m.ID)
	if err != nil || disk.Name != "renamed" {
		t.Fatalf("post-launch refusal changed the saved name: %+v, %v", disk, err)
	}
}

func TestPendingControlLockWaitCanJoinAfterCancellation(t *testing.T) {
	m, p, _ := startupControlFixture(t)
	unlock, err := lockSession(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	done := ownedStartupWorker(t, func() error { return p.rename("cannot-save") }, p.cancel)
	p.cancel()
	if err := done.wait(t); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled canonical lock wait = %v", err)
	}
	disk, err := LoadManifest(m.ID)
	if err != nil || disk.Name != m.Name {
		t.Fatalf("cancelled pending rename wrote a name: %+v, %v", disk, err)
	}
}

func TestPendingControlTransfersTheSameEndpointsToTheRunningHolder(t *testing.T) {
	m, p, _ := startupControlFixture(t)
	before, err := os.Lstat(paths.Socket(m.ID))
	if err != nil {
		t.Fatal(err)
	}
	h := &Holder{id: m.ID, m: m, log: p.peer.log, clients: map[*hclient]struct{}{}}
	p.prepare(h, false)
	if err := p.beginAgent(); err != nil {
		t.Fatal(err)
	}
	p.activate(h)
	if _, err := HolderRequest(m.ID, "sockets", nil, time.Second); err != nil {
		t.Fatalf("running holder did not inherit the existing endpoint: %v", err)
	}
	after, err := os.Lstat(paths.Socket(m.ID))
	if err != nil || !os.SameFile(before, after) || h.ln != p.peer.ln || h.hookLn != p.peer.hookLn {
		t.Fatalf("activation replaced a control endpoint: %v", err)
	}
	if _, err := HolderRequest(m.ID, cancelPendingStartOp, nil, time.Second); err == nil || p.ctx.Err() != nil {
		t.Fatalf("the pre-launch cancellation op stopped a running owner: %v, %v", err, p.ctx.Err())
	}
}

func TestPendingControlPrelaunchCancelAcknowledgesOnlyAfterRelease(t *testing.T) {
	m, p, finish := startupControlFixture(t)
	stop := ownedStartupWorker(t, func() error {
		_, err := HolderRequest(m.ID, cancelPendingStartOp, nil, 2*time.Second)
		return err
	}, finish)
	awaitStartupTest(t, p.ctx.Done())
	if !holderLockHeld(m.ID) {
		t.Fatal("pending cancellation prematurely released its owner")
	}
	select {
	case <-stop.done:
		t.Fatalf("pending cancellation acknowledged before cleanup: %v", stop.result)
	default:
	}
	finish()
	if err := stop.wait(t); err != nil {
		t.Fatal(err)
	}
	assertPendingStopReleased(t, m.ID)
}
