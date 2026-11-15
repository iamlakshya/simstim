package client

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestHostSavesKeepConcurrentChanges(t *testing.T) {
	sshDirs(t)
	firstReady, releaseFirst := make(chan struct{}), make(chan struct{})
	waiting := make(chan struct{})
	var release, signal sync.Once
	defer release.Do(func() { close(releaseFirst) })
	oldSave, oldWait := beforeHostsSave, hostsFileWaiting
	defer func() { beforeHostsSave, hostsFileWaiting = oldSave, oldWait }()
	var saves atomic.Int32
	beforeHostsSave = func() {
		if saves.Add(1) == 1 {
			close(firstReady)
			<-releaseFirst
		}
	}
	hostsFileWaiting = func() { signal.Do(func() { close(waiting) }) }
	first := make(chan error, 1)
	go func() { first <- SaveHost(Host{Name: "alpha", SSH: "alpha.example"}) }()
	select {
	case <-firstReady:
	case <-time.After(2 * time.Second):
		t.Fatal("first save did not reach its write")
	}
	second := make(chan error, 1)
	go func() { second <- SaveHost(Host{Name: "beta", SSH: "beta.example"}) }()
	var secondErr error
	secondFinished := false
	select {
	case <-waiting:
	case secondErr = <-second:
		// A writer without the lock gets here before the first save.
		// Releasing the first now deterministically exposes a lost update.
		secondFinished = true
	case <-time.After(2 * time.Second):
		t.Fatal("second save neither waited nor completed")
	}
	release.Do(func() { close(releaseFirst) })
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if !secondFinished {
		secondErr = <-second
	}
	if secondErr != nil {
		t.Fatal(secondErr)
	}
	hosts, err := LoadHosts()
	if err != nil || len(hosts) != 2 || hosts[0].Name != "alpha" || hosts[1].Name != "beta" {
		t.Fatalf("concurrent saves lost a host: %+v, %v", hosts, err)
	}
}

func TestHostFileLockHonoursContext(t *testing.T) {
	sshDirs(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if unlock, err := lockHostsFile(ctx); !errors.Is(err, context.Canceled) {
		if unlock != nil {
			unlock()
		}
		t.Fatalf("cancelled lock = %v", err)
	}
	unlock, err := lockHostsFile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel = context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := updateHostsFile(ctx, func(h []Host) ([]Host, hostFileHooks, error) { return h, hostFileHooks{}, nil }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("contended update = %v", err)
	}
}

func TestWrapperReplacementKeepsAnOpenedSnapshot(t *testing.T) {
	sshDirs(t)
	h := Host{Name: "box", SSH: "box.example"}
	path, err := writeWrapper(h)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := writeConfig(path, `host "box"`, "/another/config", true, true); err != nil {
		t.Fatal(err)
	}
	snapshot, err := io.ReadAll(f)
	if err != nil || string(snapshot) != string(before) {
		t.Fatalf("replacement changed the open config snapshot: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(after), `Include "/another/config"`) {
		t.Fatalf("new config was not published: %q, %v", after, err)
	}
}

func TestForgetKeepsACaseTwinsWrappers(t *testing.T) {
	sshDirs(t)
	h := Host{Name: "box", SSH: "box.example"}
	twin := Host{Name: "BOX", SSH: "other.example"}
	if err := SaveHosts(context.Background(), []Host{h, twin}); err != nil {
		t.Fatal(err)
	}
	path, err := writeWrapper(h)
	if err != nil {
		t.Fatal(err)
	}
	legacy := oldWrapperPath(h.Name)
	if _, err := writeConfig(legacy, `host "box"`, "", true, true); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := ForgetHost(twin.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("forget removed the case twin's keyed config: %v", err)
	}
	after, err := os.ReadFile(legacy)
	if err != nil || string(after) != string(before) {
		t.Fatalf("forget removed the case twin's old config: %v", err)
	}
}
