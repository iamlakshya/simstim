package tui

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/client"
)

type retryPinOwner struct {
	name  string
	abort func()
	prev  *retryPinOwner
}

type retryPinOwnerKey struct{}

func TestRealWizardBackendUsesOwnedStages(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var be Backend = realBackend{}
	owner, ok := be.(pinOwnerBackend)
	if !ok {
		t.Fatal("the real wizard backend does not support owned staging and saving")
	}
	ctx, abortA, err := owner.StagePinOwned(context.Background(), "box")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(abortA)
	_, abortB, err := owner.StagePinOwned(context.Background(), "box")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(abortB)
	before := wizardStageFiles(t)
	h := client.Host{Name: "box"}
	_, err = owner.UpdateHostsContext(ctx, client.HostChange{Kind: "wizard test", Now: &h, Pin: true})
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Kind != client.KindCancelled {
		t.Fatalf("real backend save = %#v, want obsolete owner cancellation", err)
	}
	abortA()
	after := wizardStageFiles(t)
	if len(before) != 1 || len(after) != 1 || before[0] != after[0] {
		t.Fatalf("real backend touched the replacement: before %q, after %q", before, after)
	}
}

func (b *retryStageBackend) StagePinOwned(ctx context.Context, name string) (context.Context, func(), error) {
	if b.entered != nil {
		close(b.entered)
	}
	if b.gate != nil {
		select {
		case <-b.gate:
		case <-ctx.Done():
			return ctx, func() {}, ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return ctx, func() {}, err
	}
	if b.stageErr != nil {
		return ctx, func() {}, b.stageErr
	}
	bound, abort, err := client.StagePinOwned(ctx, name)
	if err != nil {
		return bound, abort, err
	}
	prior, _ := ctx.Value(retryPinOwnerKey{}).(*retryPinOwner)
	owner := &retryPinOwner{name: name, prev: prior}
	var once sync.Once
	owner.abort = func() {
		once.Do(func() {
			abort()
			b.mu.Lock()
			if b.owners[name] == owner {
				delete(b.owners, name)
				delete(b.staged, name)
			}
			b.mu.Unlock()
			b.record("abort owned %s", name)
		})
	}
	b.record("stage %s", name)
	b.mu.Lock()
	if b.owners == nil {
		b.owners = map[string]*retryPinOwner{}
	}
	if b.staged == nil {
		b.staged = map[string]string{}
	}
	b.owners[name], b.staged[name] = owner, ""
	b.mu.Unlock()
	return context.WithValue(bound, retryPinOwnerKey{}, owner), owner.abort, nil
}

func (b *retryStageBackend) pinOwner(ctx context.Context, name string) (*retryPinOwner, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for owner, _ := ctx.Value(retryPinOwnerKey{}).(*retryPinOwner); owner != nil; owner = owner.prev {
		if owner.name != name {
			continue
		}
		b.mu.Lock()
		current := b.owners[name] == owner
		b.mu.Unlock()
		if !current {
			return nil, &client.UserError{Msg: "the wizard's host key stage changed", Hint: "Close the wizard, then add the host again.", Code: "cancelled", Kind: client.KindCancelled}
		}
		return owner, nil
	}
	b.mu.Lock()
	current := b.owners[name]
	b.mu.Unlock()
	if current != nil && current.abort != nil {
		return nil, &client.UserError{Msg: "the wizard lost its owned stage context", Hint: "Close the wizard, then add the host again.", Code: "cancelled", Kind: client.KindCancelled}
	}
	return nil, nil
}

func (b *retryStageBackend) PinHostKey(ctx context.Context, h client.Host, want string) (client.Pin, error) {
	if _, err := b.pinOwner(ctx, h.Name); err != nil {
		return client.Pin{}, err
	}
	return b.fakeBackend.PinHostKey(ctx, h, want)
}

func (b *retryStageBackend) Probe(ctx context.Context, h client.Host) (client.Platform, error) {
	if _, err := b.pinOwner(ctx, h.Name); err != nil {
		return client.Platform{}, err
	}
	return b.fakeBackend.Probe(ctx, h)
}

func (b *retryStageBackend) Install(ctx context.Context, h client.Host, p client.Platform) error {
	if _, err := b.pinOwner(ctx, h.Name); err != nil {
		return err
	}
	if b.installing != nil {
		close(b.installing)
	}
	if b.installGate != nil {
		select {
		case <-b.installGate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return b.fakeBackend.Install(ctx, h, p)
}

func (b *retryStageBackend) Dial(ctx context.Context, h client.Host) (Conn, error) {
	if _, err := b.pinOwner(ctx, h.Name); err != nil {
		return nil, err
	}
	return b.fakeBackend.Dial(ctx, h)
}

func (b *retryStageBackend) SaveHostPinContext(ctx context.Context, h client.Host) error {
	owner, err := b.pinOwner(ctx, h.Name)
	if err != nil {
		return err
	}
	if owner != nil {
		defer owner.abort()
	}
	return b.fakeBackend.SaveHostPinContext(ctx, h)
}

func TestWizardOldOwnerKeepsAReplacementStage(t *testing.T) {
	for _, test := range []struct {
		stop    string
		applied bool
	}{{"esc", true}, {"close", true}, {"esc", false}, {"close", false}} {
		name := test.stop + "/before-result"
		if test.applied {
			name = test.stop + "/after-result"
		}
		t.Run(name, func(t *testing.T) {
			m, w, be := stageRetryWizard(t)
			msg := w.probe(m)().(probeMsg)
			if test.applied {
				w.onProbe(m, msg)
			}
			before := wizardStageFiles(t)
			if len(before) != 1 {
				t.Fatalf("first stage = %q", before)
			}
			if _, _, err := be.StagePinOwned(context.Background(), w.host.Name); err != nil {
				t.Fatal(err)
			}
			after := wizardStageFiles(t)
			if len(after) != 1 || before[0] == after[0] {
				t.Fatalf("replacement stage = %q", after)
			}
			if test.stop == "esc" {
				if cmd := w.back(m); cmd != nil {
					cmd()
				}
			} else {
				m.Close()
			}
			assertWizardStage(t, be, true)
			for _, call := range be.callList() {
				if call == "abort box" {
					t.Fatal("the old wizard used an unowned abort")
				}
			}
		})
	}
}

func TestWizardOwnedStageSurvivesStepDeadlines(t *testing.T) {
	m, w, be := stageRetryWizard(t)
	be.probe = client.Platform{OS: "linux", Arch: "arm64"}
	be.pinUnknown = false
	w.onProbe(m, w.probe(m)().(probeMsg))
	if w.err != nil || w.pinScope == nil {
		t.Fatalf("probe = %v, scope %v", w.err, w.pinScope)
	}
	if w.pinScope.ctx.Err() == nil {
		t.Fatal("the probe operation context was not released")
	}
	msg := w.install(m)().(installMsg)
	if msg.err != nil {
		t.Fatalf("install reused the cancelled probe context: %v", msg.err)
	}
	setup := w.onInstall(m, msg)
	setupResult := setup().(setupMsg)
	if setupResult.err != nil {
		t.Fatalf("setup lost the owned stage context: %v", setupResult.err)
	}
	save := w.onSetupDone(m, setupResult)
	result := save().(hostSavedMsg)
	if result.err != nil {
		t.Fatalf("save lost the owned stage context: %v", result.err)
	}
	w.onSaved(m, result)
	if !w.saved || w.pinScope != nil || len(wizardStageFiles(t)) != 0 {
		t.Fatalf("saved %v, scope %v, files %q", w.saved, w.pinScope, wizardStageFiles(t))
	}
}

func TestWizardOwnedStageHonoursModelCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	m, w, be := stageRetryWizardContext(t, ctx)
	be.pinUnknown = false
	be.probe = client.Platform{OS: "linux", Arch: "arm64"}
	w.onProbe(m, w.probe(m)().(probeMsg))
	gate := make(chan struct{})
	defer close(gate)
	be.installGate, be.installing = gate, make(chan struct{})
	cmd := w.install(m)
	done := make(chan installMsg, 1)
	go func() { done <- cmd().(installMsg) }()
	select {
	case <-be.installing:
	case <-time.After(5 * time.Second):
		t.Fatal("install did not enter the owned backend")
	}
	cancel()
	select {
	case msg := <-done:
		if !errors.Is(msg.err, context.Canceled) {
			t.Fatalf("install = %v, want parent cancellation", msg.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("install did not finish after parent cancellation")
	}
}

func TestWizardOwnedStageKeepsTheCurrentCancellationCause(t *testing.T) {
	m, w, be := stageRetryWizard(t)
	openUncheckedWizardStage(t, m, w, be)
	parent, cancel := context.WithCancelCause(m.ctx)
	want := errors.New("the current launcher ended")
	ctx, finish := w.operationContext(parent)
	t.Cleanup(finish)
	cancel(want)
	if !errors.Is(context.Cause(ctx), want) {
		t.Fatalf("new step cause = %v, want current launcher cause", context.Cause(ctx))
	}
}

func TestWizardLaterStepsRejectAReplacementStage(t *testing.T) {
	for _, step := range []string{"probe", "install", "setup", "save"} {
		t.Run(step, func(t *testing.T) {
			m, w, be := stageRetryWizard(t)
			be.pinUnknown = false
			be.probe = client.Platform{OS: "linux", Arch: "arm64"}
			w.onProbe(m, w.probe(m)().(probeMsg))
			if w.err != nil || w.pinScope == nil {
				t.Fatalf("first probe = %v, scope %v", w.err, w.pinScope)
			}
			ctx, abort, err := be.StagePinOwned(context.Background(), w.host.Name)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(abort)
			if _, err := be.PinHostKey(ctx, w.host, ""); err != nil {
				t.Fatal(err)
			}
			switch step {
			case "probe":
				err = w.probe(m)().(probeMsg).err
			case "install":
				err = w.install(m)().(installMsg).err
			case "setup":
				err = w.setup(m)().(setupMsg).err
			case "save":
				err = w.save(m)().(hostSavedMsg).err
			}
			var ue *client.UserError
			if !errors.As(err, &ue) || ue.Kind != client.KindCancelled {
				t.Fatalf("%s = %#v, want obsolete owner cancellation", step, err)
			}
			assertWizardStage(t, be, true)
			hosts, loadErr := be.LoadHosts(context.Background())
			if loadErr != nil || len(hosts) != 0 {
				t.Fatalf("obsolete owner saved a host: %v, %v", hosts, loadErr)
			}
		})
	}
}

func TestWizardRetryAndRenameRetireOnlyPriorOwnedStage(t *testing.T) {
	m, w, be := stageRetryWizard(t)
	openUncheckedWizardStage(t, m, w, be)
	first := wizardStageFiles(t)
	w.onProbe(m, w.probe(m)().(probeMsg))
	second := wizardStageFiles(t)
	if len(second) != 1 || second[0] == first[0] {
		t.Fatalf("retry stage = %q, first %q", second, first)
	}
	cleanup := w.back(m)
	w.host = client.Host{Name: "renamed", SSH: "renamed"}
	w.onProbe(m, w.probe(m)().(probeMsg))
	third := wizardStageFiles(t)
	if len(third) != 1 || !strings.Contains(third[0], client.HostFileKey("renamed")) {
		t.Fatalf("rename left the earlier stage: %q", third)
	}
	if cleanup != nil {
		cleanup()
	}
	if after := wizardStageFiles(t); len(after) != 1 || after[0] != third[0] {
		t.Fatalf("an old deferred cleanup removed the renamed stage: %q", after)
	}
	if cmd := w.back(m); cmd != nil {
		cmd()
	}
	if files := wizardStageFiles(t); len(files) != 0 {
		t.Fatalf("renamed wizard left stages: %q", files)
	}
}

func TestWizardCloseCleansDroppedRetireCommands(t *testing.T) {
	m, w, be := stageRetryWizard(t)
	openUncheckedWizardStage(t, m, w, be)
	_ = w.back(m)
	_ = w.back(m)
	if m.modal != nil {
		t.Fatal("the second Esc did not close the wizard")
	}
	m.Close()
	assertWizardStage(t, be, false)
	m.closeAfterProgram()
	assertWizardStage(t, be, false)
}

func wizardStageFiles(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(client.ConfigDir(), "ssh", ".known_hosts-*-staged-*"))
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func (b *retryStageBackend) UpdateHostsContext(ctx context.Context, c client.HostChange) (error, error) {
	owner, err := b.pinOwner(ctx, c.Now.Name)
	if err != nil {
		return nil, err
	}
	if owner != nil {
		defer owner.abort()
	}
	return b.fakeBackend.UpdateHostsContext(ctx, c)
}

func (b *retryStageBackend) UpdateHostsForPrepare(ctx context.Context, c client.HostChange) (*wizardPrepareTarget, error, error) {
	owner, err := b.pinOwner(ctx, c.Now.Name)
	if err != nil {
		return nil, nil, err
	}
	if owner != nil {
		defer owner.abort()
	}
	return b.fakeBackend.UpdateHostsForPrepare(ctx, c)
}

func TestWizardSaveBackendKeepsCancellationAfterOwnerCheck(t *testing.T) {
	for _, operation := range []string{"pin", "host"} {
		t.Run(operation, func(t *testing.T) {
			be := &retryStageBackend{fakeBackend: newFakeBackend()}
			gate := make(chan struct{})
			be.saveGate, be.saving = gate, make(chan struct{})
			defer close(gate)
			ctx, cancel := context.WithCancel(context.WithValue(context.Background(), handoffContextKey{}, "owned save"))
			defer cancel()
			h := client.Host{Name: "box", SSH: "box"}
			done := make(chan error, 1)
			go func() {
				if operation == "pin" {
					done <- be.SaveHostPinContext(ctx, h)
				} else {
					_, err := be.UpdateHostsContext(ctx, client.HostChange{Kind: "wizard test", Now: &h})
					done <- err
				}
			}()
			select {
			case <-be.saving:
			case <-time.After(5 * time.Second):
				t.Fatal("save did not pass the owner check")
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("save = %v, want cancellation", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the backend lost cancellation after its owner check")
			}
			be.mu.Lock()
			defer be.mu.Unlock()
			if be.saveCtx != ctx || be.saveCtx.Value(handoffContextKey{}) != "owned save" || len(be.hosts) != 0 {
				t.Fatal("the backend replaced the save context or saved after cancellation")
			}
		})
	}
}
