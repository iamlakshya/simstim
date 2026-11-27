package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/client/helpers"
	"github.com/iamlakshya/simstim/internal/version"
)

// The lock of a host and of hosts.toml, as the commands use them
// (docs/contributing/cli-spec.md 11.1 and 11.3), and sameHost's causes.

// freeLock fails the test unless the lock of box is free: the command
// before it let it go. The garbage collector is off in the tests that use
// it, so a lock file that a command forgot to close stays held.
func freeLock(t *testing.T, after string) {
	t.Helper()
	_, unlock, err := client.LockHost(context.Background(), "box", "a test")
	if err != nil {
		t.Fatalf("after %s, the lock of box: %v", after, err)
	}
	unlock()
}

// Each command lets the lock go when it ends: after host create, host set,
// a host rm that refused and one that worked.
func TestCommandsLetTheLockGo(t *testing.T) {
	defer debug.SetGCPercent(debug.SetGCPercent(-1))
	_, tr := withFake(t, nil)
	p, _ := findProvider("fake")
	if err := createHost(context.Background(), p, quietRequest("box")); err != nil {
		t.Fatal(err)
	}
	freeLock(t, "host create")
	if err := setHost(context.Background(), io.Discard, "box", []string{"idle=2h"}); err != nil {
		t.Fatal(err)
	}
	freeLock(t, "host set")
	tr.SetSessions([]client.Session{{ID: "abcd2345", Name: "busy", State: api.StateWorking, Alive: true}})
	if err := removeHost(context.Background(), quietPrompts(context.Background()), destroyOpts("box", false)); err == nil {
		t.Fatal("host rm deleted a machine with a working session")
	}
	freeLock(t, "a host rm that refused")
	if err := removeHost(context.Background(), quietPrompts(context.Background()), destroyOpts("box", true)); err != nil {
		t.Fatal(err)
	}
	// After removal, box is no longer saved so LockHost refuses as unknown,
	// not busy; a fresh creation context acquires and releases its lock cleanly.
	var ue *client.UserError
	if _, _, err := client.LockHost(context.Background(), "box", "a test"); !errors.As(err, &ue) || ue.Code == "host_busy" {
		t.Fatalf("after a host rm that worked, LockHost(box) = %v, want unknown host (not host_busy)", err)
	}
	_, unlock, err := client.BeginHostCreationContext(context.Background(), client.HostCreationRequest{Name: "box", Provider: "fake"})
	if err != nil {
		t.Fatalf("after a host rm that worked, the creation lock of box: %v", err)
	}
	unlock()
}

// A read of a host by a client built with go install, with nothing in its
// cache, never downloads the helpers: host show answers at once, without
// a request, and says to run host upgrade.
func TestHostShowNeverDownloads(t *testing.T) {
	_, tr := withFake(t, nil)
	if err := saveHost(client.Host{Name: "box", Machine: &client.Machine{Provider: "fake", ID: "fake-box"}}); err != nil {
		t.Fatal(err)
	}
	tr.Helper = "0.0.0-old"
	var requests atomic.Int32
	never := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
		<-never
	}))
	t.Cleanup(func() { close(never); srv.Close() })
	t.Setenv(brand.Env("RELEASES_URL"), srv.URL)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Cleanup(helpers.HideEmbedded())
	start := time.Now()
	out, errOut, code := mainRun(t, "host", "show", "box")
	if code != 0 || requests.Load() != 0 || time.Since(start) > 10*time.Second ||
		!strings.Contains(out, "box did not answer: the helper "+version.Version+" is not on box") || errOut != "Try: "+brand.Name+" host upgrade box\n" {
		t.Errorf("exit %d, %d requests, %s\nstdout %q\nstderr %q", code, requests.Load(), time.Since(start), out, errOut)
	}
}

// sameHost keeps the error of a hosts.toml that does not decode, says
// "changed" for a host without a machine, and counts an empty table or
// list the same as none, as the save of hosts.toml drops both.
func TestSameHostSaysTheRightCause(t *testing.T) {
	withFake(t, nil)
	path := filepath.Join(client.ConfigDir(), "hosts.toml")
	plain := "[[host]]\nname = \"mine\"\nssh = \"me@example.com\"\nsecrets = []\n[host.labels]\n"
	write := func(s string) {
		_ = os.MkdirAll(filepath.Dir(path), 0o700)
		if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := beforeHostLock
	t.Cleanup(func() { beforeHostLock = old })

	write(plain)
	beforeHostLock = func(string) { write("[[host\nname = 1\n") }
	err := removeHost(context.Background(), quietPrompts(context.Background()), rmOpts{name: "mine"})
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Code != "bad_config" || !strings.Contains(ue.Msg, "line ") {
		t.Errorf("a broken hosts.toml: %+v", err)
	}

	write(plain)
	beforeHostLock = func(string) { _ = saveHost(client.Host{Name: "mine", SSH: "me@example.org"}) }
	err = removeHost(context.Background(), quietPrompts(context.Background()), rmOpts{name: "mine"})
	if !errors.As(err, &ue) || ue.Msg != "mine changed while host rm looked at it, so it changed nothing" {
		t.Errorf("a changed host without a machine: %+v", err)
	}

	write(plain)
	beforeHostLock = func(string) { _ = saveHost(client.Host{Name: "other", SSH: "other"}) }
	var said bytes.Buffer
	p := quietPrompts(context.Background())
	p.out = &said
	if err := removeHost(context.Background(), p, rmOpts{name: "mine"}); err != nil || !strings.HasPrefix(said.String(), "Forgot mine.") {
		t.Errorf("an empty table and list, and the save of another host: %v, %q", err, said.String())
	}
}

// host create of one host and host rm of another at once: both changes
// are in hosts.toml.
func TestCreateAndRmOfTwoHostsAtOnce(t *testing.T) {
	withFake(t, nil)
	p, _ := findProvider("fake")
	for i := range 10 {
		gone, made := fmt.Sprintf("gone%d", i), fmt.Sprintf("made%d", i)
		if err := saveHost(client.Host{Name: gone, SSH: gone}); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		var rmErr, createErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			rmErr = removeHost(context.Background(), quietPrompts(context.Background()), rmOpts{name: gone})
		}()
		go func() {
			defer wg.Done()
			createErr = createHost(context.Background(), p, quietRequest(made))
		}()
		wg.Wait()
		if rmErr != nil {
			t.Fatalf("run %d: rm %v (create %v)", i, rmErr, createErr)
		}
		if _, err := client.FindHost(gone); err == nil {
			t.Errorf("run %d: the host that rm forgot is back", i)
		}
		if createErr == nil {
			if _, err := client.FindHost(made); err != nil {
				t.Errorf("run %d: the host that create made is lost", i)
			}
			continue
		}
		// Canonical membership writes rotate hosts.toml's generation: if rm
		// landed while create held its creation or registration observation,
		// create must refuse with host_state instead of overwriting hosts.toml.
		var ue *client.UserError
		if !errors.As(createErr, &ue) || (ue.Code != "host_state" && ue.Code != "setup_failed") || !strings.Contains(createErr.Error(), "saved hosts changed after the original observation") {
			t.Fatalf("run %d: unexpected create error %v", i, createErr)
		}
	}
}
