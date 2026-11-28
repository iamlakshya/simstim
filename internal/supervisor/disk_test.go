package supervisor_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/supervisor"
)

func TestDiskMaxStopsSandboxWithDiskFull(t *testing.T) {
	hosttest.New(t)
	id := "disk2345"
	seedWalledManifest(t, id, "shell")
	m, err := host.LoadManifest(id)
	if err != nil {
		t.Fatal(err)
	}
	m.Limits.DiskMax = 1024 // 1 KiB cap
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	bigFile := filepath.Join(paths.SessionDir(id), "big.bin")
	if err := os.WriteFile(bigFile, make([]byte, 4096), 0o600); err != nil {
		t.Fatal(err)
	}

	sb := newFakeSandbox(t)
	done := runSupervisorBG(t, id, sb, supervisor.Options{
		HeartbeatTimeout:  5 * time.Second,
		TickInterval:      50 * time.Millisecond,
		StopGrace:         100 * time.Millisecond,
		DiskCheckInterval: 50 * time.Millisecond,
		DiskUsageFunc:     func(string) (int64, int64, bool) { return 50 << 30, 100 << 30, true },
	})
	startFakeGuestLink(t, sb)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunWith returned error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("supervisor did not stop on disk_max breach")
	}

	aud, err := os.ReadFile(paths.AuditLog(id))
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	if !strings.Contains(string(aud), `"event":"disk_full"`) {
		t.Errorf("audit log missing disk_full event:\n%s", aud)
	}
}

func TestLowFreeSpaceStopsSandboxWithDiskFull(t *testing.T) {
	hosttest.New(t)
	id := "free2345"
	seedWalledManifest(t, id, "shell")

	sb := newFakeSandbox(t)
	done := runSupervisorBG(t, id, sb, supervisor.Options{
		HeartbeatTimeout:  5 * time.Second,
		TickInterval:      50 * time.Millisecond,
		StopGrace:         100 * time.Millisecond,
		DiskCheckInterval: 50 * time.Millisecond,
		// 4% free (< 5% threshold).
		DiskUsageFunc: func(string) (int64, int64, bool) { return 4 << 30, 100 << 30, true },
	})
	startFakeGuestLink(t, sb)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunWith returned error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("supervisor did not stop on <5% host free space")
	}

	aud, err := os.ReadFile(paths.AuditLog(id))
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	if !strings.Contains(string(aud), `"event":"disk_full"`) {
		t.Errorf("audit log missing disk_full event:\n%s", aud)
	}
}
