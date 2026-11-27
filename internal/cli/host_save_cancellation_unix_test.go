//go:build unix

package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/client/clienttest"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/onboard"
)

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
		if !errors.As(err, &ue) || ue.Code != "cancelled" || ue.Kind != client.KindCancelled ||
			!strings.Contains(ue.Msg, made.Machine.ID) || !strings.Contains(ue.Msg, "may remain") {
			t.Fatalf("cancelled staged-pin save = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the save wait did not honour cancellation")
	}
	releaseHosts()
	// Cancelled creation never auto-destroys the unsaved machine; it reports
	// the exact resource, retires its staged pin, and leaves hosts.toml untouched.
	if calls := machine.Calls(); len(calls) != 0 {
		t.Fatalf("cancelled staged-pin save unexpectedly destroyed machine: %q", calls)
	}
	staged, err = filepath.Glob(filepath.Join(client.ConfigDir(), "ssh", ".known_hosts-*-staged-*"))
	if err != nil || len(staged) != 0 || client.HasPin("box") {
		t.Fatalf("cancelled save left staged or published pins: %q, %v, %v", staged, client.HasPin("box"), err)
	}
	if hosts, err := client.LoadHosts(); err != nil || len(hosts) != 0 {
		t.Fatalf("cancelled save wrote hosts: %+v, %v", hosts, err)
	}
}
