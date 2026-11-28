//go:build linux

package host_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/wall/chv"
)

// TestMicroVMHostLifecycleLiveKVM exercises the full host.New -> host.Stop ->
// host.Resume (wake) -> host.Delete lifecycle on a real Linux KVM host with
// Cloud Hypervisor, supervisor and egress processes (P3.6).
// Skipped unless CHV_E2E_HOST_LIFECYCLE=1 and CHV_E2E_HOST_BIN are set.
func TestMicroVMHostLifecycleLiveKVM(t *testing.T) {
	if os.Getenv("CHV_E2E_HOST_LIFECYCLE") != "1" {
		t.Skip("set CHV_E2E_HOST_LIFECYCLE=1 on a KVM host to run full host microvm lifecycle tests")
	}
	hostBin := os.Getenv("CHV_E2E_HOST_BIN")
	if hostBin == "" {
		t.Fatal("CHV_E2E_HOST_BIN must point to the static host helper binary")
	}
	for _, pair := range [][2]string{
		{"XDG_DATA_HOME", "CHV_E2E_DATA_HOME"},
		{"XDG_STATE_HOME", "CHV_E2E_STATE_HOME"},
		{"XDG_CONFIG_HOME", "CHV_E2E_CONFIG_HOME"},
		{"XDG_RUNTIME_DIR", "CHV_E2E_XDG_RUNTIME_DIR"},
	} {
		if v := os.Getenv(pair[1]); v != "" {
			t.Setenv(pair[0], v)
		}
	}
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")
	restoreExe := host.SetExecutable(hostBin)
	defer restoreExe()

	const diskCap = 32 << 20
	guestCmd := `if [ -f /work/boot_count ]; then n=$(cat /work/boot_count); else n=0; fi; n=$((n+1)); echo "$n" > /work/boot_count; sync; echo "BOOT_COUNT=$n"; sleep 300`

	info, err := host.New(host.NewArgs{
		Name:      "p36-live",
		Agent:     "shell",
		Argv:      []string{"/bin/sh", "-c", guestCmd},
		Wall:      "microvm",
		MemoryMax: 512 << 20,
		CPUs:      2,
		DiskMax:   diskCap,
		Cols:      80,
		Rows:      24,
	})
	if err != nil {
		t.Fatalf("host.New microvm: %v", err)
	}
	sid := info.ID
	defer func() { _, _ = host.Delete(sid) }()
	t.Logf("NEW_OK id=%s wall=%+v alive=%v state=%s", sid, info.Wall, info.Alive, info.State)
	if !info.Alive || info.Wall == nil || info.Wall.Level != api.LevelMicroVM || info.Wall.Driver != chv.DriverName {
		t.Fatalf("unexpected New info: %+v", info)
	}

	peek := func(want string) string {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		var last string
		for time.Now().Before(deadline) {
			raw, perr := host.HolderRequest(sid, "peek", map[string]int{"bytes": 4096}, 2*time.Second)
			if perr == nil {
				var out struct {
					Data []byte `json:"data"`
				}
				_ = json.Unmarshal(raw, &out)
				last = string(out.Data)
				if strings.Contains(last, want) {
					return last
				}
			}
			time.Sleep(150 * time.Millisecond)
		}
		t.Fatalf("peek never showed %q; last=%q", want, last)
		return last
	}

	out1 := peek("BOOT_COUNT=1")
	t.Logf("PEEK_BOOT1=%q", strings.TrimSpace(out1))

	// Verify supervisor, egress and cloud-hypervisor are all running.
	procs := matchingProcs(sid)
	t.Logf("PROCS_RUNNING:\n%s", strings.Join(procs, "\n"))
	joined := strings.Join(procs, "\n")
	for _, want := range []string{"supervise", "egress", "cloud-hypervisor"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected %q running for session %s, got:\n%s", want, sid, joined)
		}
	}

	// Verify disk_max cap and idle_stop off by default.
	m1, err := host.LoadManifest(sid)
	if err != nil {
		t.Fatal(err)
	}
	if m1.Wall.IdleStop != "" {
		t.Fatalf("expected idle_stop off by default, got %q", m1.Wall.IdleStop)
	}
	if alloc, _, err := chv.VerifySessionDiskCap(paths.SessionDisk(sid), diskCap); err != nil {
		t.Fatalf("VerifySessionDiskCap: %v", err)
	} else {
		t.Logf("DISK_ALLOC_BYTES=%d cap=%d", alloc, diskCap)
	}

	// Stop the microvm session.
	stopped, err := host.Stop(api.StopArgs{ID: sid})
	if err != nil {
		t.Fatalf("host.Stop: %v", err)
	}
	t.Logf("STOP_OK state=%s alive=%v unit=%q", stopped.Session.State, stopped.Session.Alive, stopped.Session.Unit)
	if stopped.Session.State != host.StateStopped || stopped.Session.Alive || stopped.Session.Unit != "" {
		t.Fatalf("unexpected Stop result: %+v", stopped)
	}

	// Wait briefly and confirm ZERO supervisor, egress or VMM processes remain,
	// sockets are gone, no memory snapshot was saved, and disk.img stays.
	time.Sleep(500 * time.Millisecond)
	if left := matchingProcs(sid); len(left) != 0 {
		t.Fatalf("leftover processes after stop: %v", left)
	}
	for _, sock := range []string{paths.Socket(sid), paths.VMMSocket(sid), paths.VsockSocket(sid)} {
		if _, err := os.Stat(sock); !os.IsNotExist(err) {
			t.Fatalf("socket left after stop: %s", sock)
		}
	}
	if _, err := os.Stat(paths.SessionDisk(sid)); err != nil {
		t.Fatalf("disk.img missing after stop: %v", err)
	}
	entries, _ := os.ReadDir(paths.SessionDir(sid))
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
		low := strings.ToLower(e.Name())
		if strings.Contains(low, "snap") || strings.Contains(low, "mem") {
			t.Fatalf("unexpected memory snapshot file after stop: %s", e.Name())
		}
	}
	t.Logf("SESSION_FILES_AFTER_STOP=%v", names)

	// Wake (resume) from stopped state -> cold-boots against preserved disk.img
	// with a fresh screen (no BOOT_COUNT=1 kept across stop).
	resumed, err := host.Resume(api.ResumeArgs{ID: sid})
	if err != nil {
		t.Fatalf("host.Resume (wake): %v", err)
	}
	t.Logf("WAKE_OK alive=%v state=%s why=%q", resumed.Session.Alive, resumed.Session.State, resumed.Why)
	if !resumed.Session.Alive {
		t.Fatalf("resumed microvm not alive: %+v", resumed)
	}
	out2 := peek("BOOT_COUNT=2")
	t.Logf("PEEK_BOOT2=%q", strings.TrimSpace(out2))
	if strings.Contains(out2, "BOOT_COUNT=1") {
		t.Fatalf("old screen leaked across stop into woken session: %q", out2)
	}

	// Destroy (Delete) removes all processes, sockets and session folder (including disk.img).
	del, err := host.Delete(sid)
	if err != nil || !del.Deleted {
		t.Fatalf("host.Delete = (%+v, %v)", del, err)
	}
	time.Sleep(500 * time.Millisecond)
	if left := matchingProcs(sid); len(left) != 0 {
		t.Fatalf("leftover processes after delete: %v", left)
	}
	if _, err := os.Stat(paths.SessionDir(sid)); !os.IsNotExist(err) {
		t.Fatalf("session dir still exists after delete: %v", err)
	}
	t.Log("FULL_HOST_LIFECYCLE_OK=YES")
}

func matchingProcs(sid string) []string {
	out, _ := exec.Command("pgrep", "-fa", sid).Output()
	var kept []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" || strings.Contains(line, "pgrep") || strings.Contains(line, "host.test") {
			continue
		}
		kept = append(kept, line)
	}
	return kept
}
