package client_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/client"
)

func TestHostStateDirSeparatesCaseTwins(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	a := client.HostStateDir("box")
	b := client.HostStateDir("Box")
	if a == b {
		t.Fatalf("case twins shared HostStateDir: %q", a)
	}
	if !strings.HasPrefix(filepath.Base(a), "box-") || !strings.HasPrefix(filepath.Base(b), "box-") {
		t.Fatalf("unexpected slugs: %q, %q", a, b)
	}
}

func TestLockHostContentionAndRelease(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	ctx := context.Background()
	unlock, err := client.LockHost(ctx, "box", "host rm")
	if err != nil {
		t.Fatalf("LockHost: %v", err)
	}
	_, err = client.LockHost(ctx, "box", "host set")
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Code != "host_busy" || !strings.Contains(ue.Msg, "host rm") {
		t.Fatalf("contended LockHost = %v, want host_busy naming host rm", err)
	}
	unlock()

	unlock2, err := client.LockHost(ctx, "box", "host set")
	if err != nil {
		t.Fatalf("LockHost after release: %v", err)
	}
	unlock2()
}

func TestRemovalRecordRoundTrip(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if _, ok, err := client.LoadRemovalRecord("box"); err != nil || ok {
		t.Fatalf("missing record = ok:%v err:%v", ok, err)
	}
	want := client.RemovalRecord{
		Host:           "box",
		Provider:       "lima",
		MachineID:      "box-1",
		MachineCreated: "2026-11-28T00:00:00Z",
		Destroyed:      true,
		UpdatedAt:      time.Date(2026, 11, 28, 1, 2, 3, 0, time.UTC),
	}
	if err := client.SaveRemovalRecord(want); err != nil {
		t.Fatalf("SaveRemovalRecord: %v", err)
	}
	got, ok, err := client.LoadRemovalRecord("box")
	if err != nil || !ok || got.Provider != want.Provider || got.MachineID != want.MachineID || got.MachineCreated != want.MachineCreated || !got.Destroyed {
		t.Fatalf("LoadRemovalRecord = %+v, ok=%v, err=%v", got, ok, err)
	}
	if err := client.DeleteRemovalRecord("box"); err != nil {
		t.Fatalf("DeleteRemovalRecord: %v", err)
	}
	if _, ok, err := client.LoadRemovalRecord("box"); err != nil || ok {
		t.Fatalf("after delete = ok:%v err:%v", ok, err)
	}
}

func TestSaveAndForgetHostIfUnchanged(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	ctx := context.Background()

	h1 := client.Host{Name: "box", SSH: "me@box"}
	if err := client.SaveHostIfUnchanged(ctx, nil, h1); err != nil {
		t.Fatalf("SaveHostIfUnchanged create: %v", err)
	}
	var ue *client.UserError
	if err := client.SaveHostIfUnchanged(ctx, nil, h1); !errors.As(err, &ue) || ue.Code != "host_exists" {
		t.Fatalf("duplicate create = %v, want host_exists", err)
	}

	h2 := h1
	h2.Trusted = true
	if err := client.SaveHostIfUnchanged(ctx, &h1, h2); err != nil {
		t.Fatalf("SaveHostIfUnchanged update: %v", err)
	}
	// Stale prev fails with host_changed.
	if err := client.SaveHostIfUnchanged(ctx, &h1, h2); !errors.As(err, &ue) || ue.Code != "host_changed" {
		t.Fatalf("stale update = %v, want host_changed", err)
	}
	if err := client.ForgetHostIfUnchanged(ctx, h1); !errors.As(err, &ue) || ue.Code != "host_changed" {
		t.Fatalf("stale forget = %v, want host_changed", err)
	}
	if err := client.ForgetHostIfUnchanged(ctx, h2); err != nil {
		t.Fatalf("ForgetHostIfUnchanged: %v", err)
	}
	if _, err := client.FindHost("box"); err == nil {
		t.Fatal("host still saved after ForgetHostIfUnchanged")
	}
}

func TestBuildHostDetailsSharedValues(t *testing.T) {
	h := client.Host{
		Name:      "box",
		SSH:       "dev@box.example",
		SSHConfig: "~/infra/ssh.config",
		Trusted:   true,
		WallFloor: "process",
		Labels:    map[string]string{"env": "prod", "region": "eu"},
		Secrets:   []string{"ANTHROPIC_API_KEY"},
	}
	info := &client.HostInfo{
		Distro:        "Debian 13\x1b[31m",
		OS:            "linux",
		Arch:          "amd64",
		CPUs:          4,
		MemTotal:      8 << 30,
		DiskFree:      40 << 30,
		MemoryMax:     6 << 30,
		Version:       "0.2.0",
		Agents:        map[string]string{"claude": "/usr/bin/claude", "shell": "/bin/sh"},
		AgentVersions: map[string]string{"claude": "2.1.3"},
	}
	d := client.BuildHostDetails(h, info, 2)
	if !d.Reachable || !d.Trusted || d.Target != "dev@box.example" || d.WallFloor != "process" {
		t.Fatalf("unexpected details header: %+v", d)
	}
	if strings.Contains(d.System, "\x1b") {
		t.Fatalf("System kept control byte: %q", d.System)
	}
	if d.System != "Debian 13[31m · amd64 · 4 CPUs · 8 GiB · 40 GiB free" {
		t.Errorf("System = %q", d.System)
	}
	if d.Limits != "6 GiB for sessions · 2 sessions" {
		t.Errorf("Limits = %q", d.Limits)
	}
	if len(d.Agents) != 2 || d.Agents[0] != "claude 2.1.3" || d.Agents[1] != "shell" {
		t.Errorf("Agents = %v", d.Agents)
	}
	if len(d.Labels) != 2 || d.Labels[0] != "env=prod" || d.Labels[1] != "region=eu" {
		t.Errorf("Labels = %v", d.Labels)
	}
}

func TestLockDirErrorWhenFileBlocksStateDir(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "statefile")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", blocker)
	_, err := client.LockHost(context.Background(), "box", "host rm")
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Code != "lock_failed" || !strings.Contains(ue.Hint, blocker) {
		t.Fatalf("blocked lock = %v, want lock_failed naming %s", err, blocker)
	}
}

func TestForgetHostSerializesWithConcurrentSaveHost(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	ctx := context.Background()

	// Seed 20 hosts, then concurrently forget the first 10 while adding 10 new hosts.
	var initial []client.Host
	for i := range 10 {
		initial = append(initial, client.Host{Name: "drop-" + string(rune('a'+i)), SSH: "me@drop"})
	}
	if err := client.SaveHosts(ctx, initial); err != nil {
		t.Fatalf("SaveHosts: %v", err)
	}

	errCh := make(chan error, 20)
	for i := range 10 {
		dropName := "drop-" + string(rune('a'+i))
		keepName := "keep-" + string(rune('a'+i))
		go func() {
			errCh <- client.ForgetHost(ctx, dropName)
		}()
		go func() {
			errCh <- client.SaveHost(ctx, client.Host{Name: keepName, SSH: "me@keep"})
		}()
	}
	for range 20 {
		if err := <-errCh; err != nil {
			t.Fatalf("concurrent host mutation failed: %v", err)
		}
	}
	got, err := client.LoadHosts()
	if err != nil {
		t.Fatalf("LoadHosts: %v", err)
	}
	if len(got) != 10 {
		t.Fatalf("len(hosts) = %d, want 10 (hosts: %+v)", len(got), got)
	}
	for _, h := range got {
		if !strings.HasPrefix(h.Name, "keep-") {
			t.Errorf("unexpected remaining host %q", h.Name)
		}
	}
}
