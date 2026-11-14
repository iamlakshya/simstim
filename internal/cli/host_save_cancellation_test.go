package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/client/clienttest"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/onboard"
	"github.com/iamlakshya/simstim/internal/provider"
)

type cancelledSaveMachine struct {
	client.Lifecycle
	liveCleanup bool
	bounded     bool
	heldLock    bool
	checkLock   func() bool
}

func (m *cancelledSaveMachine) Destroy(ctx context.Context) error {
	m.liveCleanup = ctx.Err() == nil
	if m.checkLock != nil {
		m.heldLock = m.checkLock()
	}
	if deadline, ok := ctx.Deadline(); ok {
		m.bounded = time.Until(deadline) > 0 && time.Until(deadline) <= 2*time.Minute
	}
	return m.Lifecycle.Destroy(ctx)
}

func observeSaveCleanup(t *testing.T, machine client.Lifecycle) *cancelledSaveMachine {
	t.Helper()
	// A separate caller has no reentrant host-lock marker from Create.
	contender := t.Context()
	return &cancelledSaveMachine{Lifecycle: machine, checkLock: func() bool {
		_, unlock, err := client.LockHost(contender, "box", "independent cleanup observer")
		if err == nil {
			unlock()
		}
		var ue *client.UserError
		return errors.As(err, &ue) && ue.Code == "host_busy"
	}}
}

type cancelCreatedProvider struct {
	fakeProvider
	cancel  context.CancelFunc
	cleanup *cancelledSaveMachine
	opened  *client.Host
}

func (p cancelCreatedProvider) Create(ctx context.Context, req provider.Request) (client.Host, error) {
	h, err := p.fakeProvider.Create(ctx, req)
	if err == nil && h.Machine != nil {
		h.Machine.ID = "provider-assigned-17"
		p.cancel()
	}
	return h, err
}

func (p cancelCreatedProvider) Open(h client.Host) (client.Lifecycle, error) {
	*p.opened = h
	return p.cleanup, nil
}

func TestCancelledCreateDeletesOnlyItsUnsavedMachine(t *testing.T) {
	machine, _ := withFake(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cleanup := observeSaveCleanup(t, machine)
	var opened client.Host
	p := cancelCreatedProvider{fakeProvider: fakeProvider{m: machine}, cancel: cancel, cleanup: cleanup, opened: &opened}
	_, err := onboard.Create(ctx, p, quietRequest("box"))
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Code != "cancelled" || ue.Kind != client.KindCancelled {
		t.Fatalf("cancelled create = %v, want the cancellation code and kind", err)
	}
	if calls := machine.Calls(); !slices.Equal(calls, []string{"destroy"}) || !cleanup.liveCleanup || !cleanup.bounded || !cleanup.heldLock {
		t.Fatalf("unsaved-machine cleanup = %q, live %v, bounded %v, held lock %v", calls, cleanup.liveCleanup, cleanup.bounded, cleanup.heldLock)
	}
	if opened.Name != "box" || opened.Machine == nil || opened.Machine.Provider != "fake" || opened.Machine.ID != "provider-assigned-17" || opened.Machine.Created == "" {
		t.Fatalf("cleanup opened a different machine: %+v", opened)
	}
	if hosts, err := client.LoadHosts(); err != nil || len(hosts) != 0 {
		t.Fatalf("cancelled create saved hosts: %+v, %v", hosts, err)
	}
}

type cancelAfterDestroy struct {
	client.Lifecycle
	cancel context.CancelFunc
}

func (m cancelAfterDestroy) Destroy(ctx context.Context) error {
	if err := m.Lifecycle.Destroy(ctx); err != nil {
		return err
	}
	m.cancel()
	return nil
}

func TestCancelledForgetAfterDestroyKeepsSavedEntry(t *testing.T) {
	_, machine, _ := withSaved(t)
	before, err := client.FindHost("box")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client.RegisterProvider("fake", func(client.Host) (client.Lifecycle, error) {
		return cancelAfterDestroy{Lifecycle: machine, cancel: cancel}, nil
	})
	t.Cleanup(func() { client.RegisterProvider("fake", (fakeProvider{m: machine}).Open) })
	p := quietPrompts(ctx)
	err = removeHost(ctx, p, destroyOpts("box", false))
	if !errors.Is(err, context.Canceled) || !slices.Contains(machine.Calls(), "destroy") {
		t.Fatalf("cancel after destroy = %v, calls %q", err, machine.Calls())
	}
	after, err := client.FindHost("box")
	if err != nil || !client.SameEntry(after, before) {
		t.Fatalf("the cancelled forget changed the saved host: %+v, %v", after, err)
	}
	changes := strings.Join(p.changes().Lines(), "\n")
	if !strings.Contains(changes, "deleted the fake machine fake-box") || strings.Contains(changes, "forgot") {
		t.Fatalf("completed changes = %q, want only the actual deletion", changes)
	}
}

type stagedSaveProvider struct {
	cancelCreatedProvider
	want    string
	ready   chan<- client.Host
	release <-chan struct{}
}

func (p stagedSaveProvider) Create(ctx context.Context, req provider.Request) (client.Host, error) {
	h, err := p.fakeProvider.Create(ctx, req)
	if err != nil {
		return h, err
	}
	h.SSH, h.Machine.ID = "box", "provider-assigned-save-wait"
	if _, err := client.PinHostKey(ctx, h, p.want, client.FirstUseAccept); err != nil {
		return client.Host{}, err
	}
	p.ready <- h
	select {
	case <-p.release:
	case <-ctx.Done():
	}
	return h, nil
}

func TestCancelledCreateSaveWaitRetiresItsStagedPin(t *testing.T) {
	machine, _ := withFake(t, nil)
	bin := t.TempDir()
	clienttest.WriteScript(t, filepath.Join(bin, "ssh"), "#!/bin/sh\nfor a; do last=$a; done\n"+keyFake+"\nexit 255\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	want := hostShows(t, os.Getenv("HOME"), "trusted")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cleanup := observeSaveCleanup(t, machine)
	var opened client.Host
	ready, release := make(chan client.Host, 1), make(chan struct{})
	letGo := sync.OnceFunc(func() { close(release) })
	p := stagedSaveProvider{cancelCreatedProvider: cancelCreatedProvider{fakeProvider: fakeProvider{m: machine}, cleanup: cleanup, opened: &opened}, want: want, ready: ready, release: release}
	var notes lockedBuffer
	ctx = client.WithNotes(ctx, &notes)
	result, joined := make(chan error, 1), make(chan struct{})
	var releaseHosts func()
	t.Cleanup(func() {
		cancel()
		letGo()
		if releaseHosts != nil {
			releaseHosts()
		}
		select {
		case <-joined:
		case <-time.After(10 * time.Second):
			t.Error("the owned create did not finish")
		}
	})
	go func() { defer close(joined); _, err := onboard.Create(ctx, p, quietRequest("box")); result <- err }()
	var made client.Host
	select {
	case made = <-ready:
	case <-time.After(10 * time.Second):
		t.Fatal("the provider did not stage its new machine's pin")
	}
	staged, err := filepath.Glob(filepath.Join(client.ConfigDir(), "ssh", ".known_hosts-*-staged-*"))
	if err != nil || len(staged) != 1 || client.HasPin("box") {
		t.Fatalf("before save wait: staged %q, published %v, %v", staged, client.HasPin("box"), err)
	}
	if info, err := os.Stat(staged[0]); err != nil || info.Size() == 0 {
		t.Fatalf("the staged pin has no key: %v", err)
	}
	releaseHosts = holdHostsToml(t)
	letGo()
	hosttest.WaitFor(t, 10*time.Second, "the staged-pin host save to wait for its lock", func() bool {
		return strings.Contains(notes.String(), "Waiting for another command that saves ")
	})
	cancel()
	select {
	case err := <-result:
		var ue *client.UserError
		if !errors.As(err, &ue) || ue.Code != "cancelled" || ue.Kind != client.KindCancelled {
			t.Fatalf("cancelled staged-pin save = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the save wait did not honour cancellation")
	}
	releaseHosts()
	if !client.SameEntry(opened, made) || !cleanup.liveCleanup || !cleanup.bounded || !cleanup.heldLock || !slices.Contains(machine.Calls(), "destroy") {
		t.Fatalf("cleanup lost the exact machine, deadline or lock: made %+v, opened %+v, cleanup %+v", made, opened, cleanup)
	}
	staged, err = filepath.Glob(filepath.Join(client.ConfigDir(), "ssh", ".known_hosts-*-staged-*"))
	if err != nil || len(staged) != 0 || client.HasPin("box") {
		t.Fatalf("cancelled save left staged or published pins: %q, %v, %v", staged, client.HasPin("box"), err)
	}
	if hosts, err := client.LoadHosts(); err != nil || len(hosts) != 0 {
		t.Fatalf("cancelled save wrote hosts: %+v, %v", hosts, err)
	}
}

func TestCancelAfterInitialCreateCommitKeepsTheSavedHost(t *testing.T) {
	machine, transport := withFake(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var committed []byte
	var readErr error
	calls := 0
	transport.OnRun = func(string) {
		calls++
		if calls == 1 {
			committed, readErr = os.ReadFile(filepath.Join(client.ConfigDir(), "hosts.toml"))
		}
		cancel()
	}
	err := createHost(ctx, fakeProvider{m: machine}, quietRequest("box"))
	if err == nil || ctx.Err() == nil || readErr != nil || len(committed) == 0 || calls != 1 {
		t.Fatalf("post-commit preparation cancel: %v, snapshot %v, calls %d", err, readErr, calls)
	}
	h, err := client.FindHost("box")
	if err != nil || h.Machine == nil || h.Machine.ID != "fake-box" || h.Machine.Created == "" {
		t.Fatalf("post-commit cancellation lost the saved machine: %+v, %v", h, err)
	}
	after, err := os.ReadFile(filepath.Join(client.ConfigDir(), "hosts.toml"))
	if err != nil || string(after) != string(committed) || slices.Contains(machine.Calls(), "destroy") {
		t.Fatalf("later cancellation wrote or deleted the committed host: %v, calls %q", err, machine.Calls())
	}
}
