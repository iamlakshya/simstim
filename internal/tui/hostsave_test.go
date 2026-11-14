package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
)

// ownFiles gives the test its own hosts.toml, home and state folder, so
// the locks of hosts are the test's own, never the user's. USERPROFILE is
// the home on Windows.
func ownFiles(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
}

// The launcher's real backend saves through client.UpdateHosts: a remove
// that the user confirmed after a host create made the host again is
// refused, and the new host stays. While a command holds the lock of the
// host, the launcher's change is busy. Only the entry that the launcher
// read is forgotten.
func TestRealBackendComparesAndLocks(t *testing.T) {
	ownFiles(t)
	shown := client.Host{Name: "box", Machine: &client.Machine{Provider: "lima", ID: "box", Created: "1"}}
	again := client.Host{Name: "box", Machine: &client.Machine{Provider: "lima", ID: "box", Created: "2"}}
	if err := client.SaveHosts(context.Background(), []client.Host{again}); err != nil {
		t.Fatal(err)
	}
	be := realBackend{}
	_, err := be.UpdateHosts(client.HostChange{Kind: "the launcher", Was: &shown})
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Code != "host_changed" || ue.Msg != "box was made again while the launcher ran, so it was not forgotten" {
		t.Fatalf("a stale remove: %+v", err)
	}
	if h, err := client.FindHost("box"); err != nil || h.Machine.Created != "2" {
		t.Fatalf("the host made again: %+v, %v", h, err)
	}
	_, unlock, err := client.LockHost(context.Background(), "box", "host rm")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := be.UpdateHosts(client.HostChange{Kind: "the launcher", Was: &again}); !errors.As(err, &ue) || ue.Code != "host_busy" {
		t.Fatalf("a remove while host rm holds the lock: %+v", err)
	}
	unlock()
	if warn, err := be.UpdateHosts(client.HostChange{Kind: "the launcher", Was: &again}); err != nil || warn != nil {
		t.Fatal(err, warn)
	}
	if hosts, err := client.LoadHosts(); err != nil || len(hosts) != 0 {
		t.Errorf("the hosts are %+v, %v", hosts, err)
	}
	if _, err := os.Stat(client.StateDir()); err != nil {
		t.Errorf("the lock is not in the test's own state folder: %v", err)
	}
}

// The launcher's forget and the clean-up of the host's record are one step
// under the lock of the host: no command of the host, such as a host
// create that would record the removal of a new machine, comes between
// them. A record that cannot be deleted is a warning: the host is
// forgotten all the same.
func TestTheLauncherForgetsAndCleansUnderOneLock(t *testing.T) {
	ownFiles(t)
	box := client.Host{Name: "box", SSH: "box"}
	if err := client.SaveHosts(context.Background(), []client.Host{box}); err != nil {
		t.Fatal(err)
	}
	rec := client.LoadRemoval(box)
	if err := rec.Record(client.RemovalDestroy); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(client.StateDir(), "hosts", "box-*", "removal.json"))
	if len(files) != 1 {
		t.Fatalf("the record: %v", files)
	}
	// A folder that is not empty stands where the record is: it cannot be
	// deleted on any system.
	if err := os.Remove(files[0]); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(files[0], "in"), 0o700); err != nil {
		t.Fatal(err)
	}
	var between error
	old := afterLauncherForget
	afterLauncherForget = func(name string) {
		_, unlock, err := client.LockHost(context.Background(), name, "host create")
		if err == nil {
			unlock()
		}
		between = err
	}
	t.Cleanup(func() { afterLauncherForget = old })
	warn, err := realBackend{}.UpdateHosts(client.HostChange{Kind: "the launcher", Was: &box})
	var ue *client.UserError
	if err != nil || !errors.As(warn, &ue) || ue.Code != "removal_record" {
		t.Fatalf("the forget: %v, warning %v", err, warn)
	}
	if !errors.As(between, &ue) || ue.Code != "host_busy" || ue.Msg != "box is busy: the launcher changes it" {
		t.Errorf("a command between the forget and the clean-up: %v", between)
	}
	if _, err := client.FindHost("box"); err == nil {
		t.Error("the host was not forgotten")
	}
}
