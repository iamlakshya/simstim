package lima

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/provider"
)

func TestSavedLifecycleKeepsOriginalLimaHomeAndInstance(t *testing.T) {
	dir := fakeLima(t, "linux")
	host, err := fixtureCreate(t, t.Context(), provider.Request{Name: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := (Provider{}).Open(host)
	if err != nil {
		t.Fatal(err)
	}
	original := host.Machine.Source.Config.Path
	foreign := t.TempDir()
	host.Machine.Source.Config.Path = filepath.Join(foreign, "foreign")
	host.Machine.ID = "foreign"
	t.Setenv("LIMA_HOME", foreign)
	if err := os.WriteFile(filepath.Join(dir, "home-log"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if status, err := lifecycle.Status(t.Context()); err != nil || status != client.MachineRunning {
		t.Fatalf("original status = %s %v", status, err)
	}
	transport, err := lifecycle.Transport(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	ssh, ok := transport.(*client.SSH)
	if !ok || ssh.Host.SSHConfig != filepath.Join(original, "ssh.config") || ssh.Host.SSH != sshAlias(provider.MachineName("dev")) {
		t.Fatalf("transport lost the original SSH route: %#v", transport)
	}
	if err := lifecycle.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Destroy(t.Context()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "home-log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, home := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if home != filepath.Dir(original) {
			t.Fatalf("saved lifecycle selected current LIMA_HOME: %q", home)
		}
	}
}

func TestSavedLifecycleRejectsReusedLimaInstanceBeforeAnyCommand(t *testing.T) {
	dir := fakeLima(t, "linux")
	host, err := fixtureCreate(t, t.Context(), provider.Request{Name: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := (Provider{}).Open(host)
	if err != nil {
		t.Fatal(err)
	}
	original := host.Machine.Source.Config.Path
	if err := os.Rename(original, original+"-retained"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(original, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "log"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Destroy(t.Context()); err == nil {
		t.Fatal("a reused Lima instance path accepted deletion")
	}
	if commands := calls(t, dir); len(commands) != 0 {
		t.Fatalf("replaced native instance reached limactl: %q", commands)
	}
	if _, err := os.Stat(original); err != nil {
		t.Fatalf("reused instance was removed: %v", err)
	}
}

func TestSavedLifecycleRejectsReturnedForeignDirectoryAndStaleRoute(t *testing.T) {
	dir := fakeLima(t, "linux")
	host, err := fixtureCreate(t, t.Context(), provider.Request{Name: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := (Provider{}).Open(host)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycle.Status(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "listed-home"), []byte(t.TempDir()), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycle.Transport(t.Context(), false); err == nil {
		t.Fatal("a previous status check hid a foreign current SSH config")
	}
	if err := lifecycle.Stop(t.Context()); err == nil {
		t.Fatal("a foreign returned instance directory accepted an effect")
	}
}

func TestSavedLimaSSHLeaseKeepsOriginalContextAndRejectsReplacement(t *testing.T) {
	fakeLima(t, "linux")
	host, err := fixtureCreate(t, t.Context(), provider.Request{Name: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := (Provider{}).Open(host)
	if err != nil {
		t.Fatal(err)
	}
	original, cancel := context.WithCancel(t.Context())
	lease, err := lifecycle.(*vm).OpenSSHNativeSource(original)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	defer lease.Close()
	if err := lease.Revalidate(t.Context()); err != nil {
		t.Fatal("the live original lease failed", err)
	}
	cancel()
	if err := lease.Revalidate(t.Context()); !errors.Is(err, context.Canceled) {
		t.Fatal("a fresh caller context renewed the original native lease", err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if err := lease.Revalidate(t.Context()); err == nil {
		t.Fatal("a closed native SSH lease was renewed")
	}
	lease, err = lifecycle.(*vm).OpenSSHNativeSource(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	path := host.Machine.Source.Config.Path
	if err := os.Rename(path, path+"-retained"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := lease.Revalidate(t.Context()); err == nil {
		t.Fatal("a live native lease accepted a reused instance path")
	}
}
