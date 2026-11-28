package supervisor_test

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/guest"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/supervisor"
	"github.com/iamlakshya/simstim/internal/wall/chv"
)

func TestStopRequestTransitionsToStoppedAndKeepsNoScreen(t *testing.T) {
	hosttest.New(t)
	id := "stop2345"
	seedWalledManifest(t, id, "shell")

	sb := newFakeSandbox(t)
	done := runSupervisorBG(t, id, sb, supervisor.Options{
		HeartbeatTimeout: 5 * time.Second,
		TickInterval:     50 * time.Millisecond,
		StopGrace:        200 * time.Millisecond,
		DiskUsageFunc:    func(string) (int64, int64, bool) { return 50 << 30, 100 << 30, true },
	})
	startFakeGuestLink(t, sb)

	res, err := host.HolderRequest(id, "stop", nil, 3*time.Second)
	if err != nil {
		t.Fatalf("stop request failed: %v", err)
	}
	if !strings.Contains(string(res), `"stopped":true`) {
		t.Fatalf("unexpected stop reply: %s", res)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunWith returned error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("supervisor did not exit after stop request")
	}

	m, err := host.LoadManifest(id)
	if err != nil {
		t.Fatal(err)
	}
	if m.Status.State != host.StateStopped || m.Run.HolderPID != 0 || m.Run.AgentPID != 0 || m.Run.Unit != "" || m.Guest != nil {
		t.Fatalf("manifest after stop = %+v, want StateStopped with cleared Run and Guest=nil", m)
	}
	if _, err := os.Stat(paths.Socket(id)); !os.IsNotExist(err) {
		t.Errorf("session socket still present after stop: %v", err)
	}
	aud, _ := os.ReadFile(paths.AuditLog(id))
	if !strings.Contains(string(aud), `"event":"stop"`) {
		t.Errorf("audit log missing stop event:\n%s", aud)
	}
}

func TestIdleStopOffByDefaultAndStopsWhenConfigured(t *testing.T) {
	t.Run("off by default", func(t *testing.T) {
		hosttest.New(t)
		id := "idl02345"
		seedWalledManifest(t, id, "shell")

		sb := newFakeSandbox(t)
		done := runSupervisorBG(t, id, sb, supervisor.Options{
			HeartbeatTimeout: 5 * time.Second,
			TickInterval:     30 * time.Millisecond,
			StopGrace:        100 * time.Millisecond,
			DiskUsageFunc:    func(string) (int64, int64, bool) { return 50 << 30, 100 << 30, true },
		})
		_, ctlW := startFakeGuestLink(t, sb)
		// Report guest idle; with default IdleStop=0 it must stay alive.
		_ = ctlW.WriteJSON(proto.TEvent, guest.Event{Type: "status", Session: id, State: host.StateIdle, Source: "hook"})
		select {
		case err := <-done:
			t.Fatalf("supervisor stopped while idle_stop was off: %v", err)
		case <-time.After(250 * time.Millisecond):
		}
		_, _ = host.HolderRequest(id, "stop", nil, 3*time.Second)
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("supervisor did not exit after stop request")
		}
	})

	t.Run("stops idle session when configured", func(t *testing.T) {
		hosttest.New(t)
		id := "idl12345"
		seedWalledManifest(t, id, "shell")

		sb := newFakeSandbox(t)
		done := runSupervisorBG(t, id, sb, supervisor.Options{
			HeartbeatTimeout: 5 * time.Second,
			TickInterval:     30 * time.Millisecond,
			StopGrace:        100 * time.Millisecond,
			IdleStop:         120 * time.Millisecond,
			DiskUsageFunc:    func(string) (int64, int64, bool) { return 50 << 30, 100 << 30, true },
		})
		_, ctlW := startFakeGuestLink(t, sb)
		// Transition guest to idle with no attached clients -> supervisor stops.
		_ = ctlW.WriteJSON(proto.TEvent, guest.Event{Type: "status", Session: id, State: host.StateIdle, Source: "hook"})

		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("RunWith returned error: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("supervisor did not stop idle session after IdleStop")
		}

		m, err := host.LoadManifest(id)
		if err != nil {
			t.Fatal(err)
		}
		if m.Status.State != host.StateStopped || !strings.Contains(m.Status.Reason, "idle for ") {
			t.Fatalf("manifest after idle_stop = %+v, want StateStopped with idle reason", m.Status)
		}
		aud, _ := os.ReadFile(paths.AuditLog(id))
		if !strings.Contains(string(aud), `"event":"idle_stop"`) {
			t.Errorf("audit log missing idle_stop event:\n%s", aud)
		}
	})
}

func TestMicroVMSparseDiskIgnoresApparentSizeButStopsOnLowFreeSpace(t *testing.T) {
	mkfs, err := chv.FindMkfsExt4(nil, nil)
	if err != nil {
		t.Skip("mkfs.ext4 not available")
	}
	hosttest.New(t)
	id := "mvdk2345"
	seedWalledManifest(t, id, "shell")
	m, err := host.LoadManifest(id)
	if err != nil {
		t.Fatal(err)
	}
	const capBytes = 32 << 20
	m.Limits.DiskMax = capBytes
	m.Wall = &host.WallRecord{Level: api.LevelMicroVM, Driver: chv.DriverName}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	// Create a real 32 MiB sparse ext4 disk (apparent size == capBytes, allocated << capBytes).
	if _, err := chv.EnsureSessionDisk(t.Context(), paths.SessionDisk(id), capBytes, mkfs); err != nil {
		t.Fatalf("EnsureSessionDisk: %v", err)
	}

	lowSpace := make(chan struct{})
	sb := newFakeSandbox(t)
	done := runSupervisorBG(t, id, sb, supervisor.Options{
		HeartbeatTimeout:  5 * time.Second,
		TickInterval:      30 * time.Millisecond,
		StopGrace:         100 * time.Millisecond,
		DiskCheckInterval: 30 * time.Millisecond,
		DiskUsageFunc: func(string) (int64, int64, bool) {
			select {
			case <-lowSpace:
				return 4 << 30, 100 << 30, true // 4% free (<5%)
			default:
				return 50 << 30, 100 << 30, true // 50% free
			}
		},
	})
	startFakeGuestLink(t, sb)

	// Confirm the sparse 32 MiB disk does NOT falsely trigger disk_full.
	select {
	case err := <-done:
		t.Fatalf("sparse microvm disk falsely triggered stop: %v", err)
	case <-time.After(200 * time.Millisecond):
	}

	// Now drop host free space below 5% -> must stop with disk_full and StateStopped.
	close(lowSpace)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunWith returned error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("microvm supervisor did not stop on <5% host free space")
	}

	m2, err := host.LoadManifest(id)
	if err != nil {
		t.Fatal(err)
	}
	if m2.Status.State != host.StateStopped || !strings.Contains(m2.Status.Reason, "disk_full") {
		t.Fatalf("manifest after <5%% free stop = %+v, want StateStopped with disk_full reason", m2.Status)
	}
	aud, _ := os.ReadFile(paths.AuditLog(id))
	if !strings.Contains(string(aud), `"event":"disk_full"`) {
		t.Errorf("audit log missing disk_full event:\n%s", aud)
	}
}

func startFakeGuestLinkWithSpawn(t *testing.T, sb *fakeSandbox) (*link.Session, *proto.Writer) {
	t.Helper()
	sess := link.New(sb.guestConn, link.Guest, link.Options{})
	t.Cleanup(func() { _ = sess.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctl, err := sess.Accept(ctx)
	if err != nil {
		t.Fatalf("accept ctl: %v", err)
	}
	r := bufio.NewReader(ctl)
	w := proto.NewWriter(ctl)
	f, err := proto.Read(r)
	if err != nil || f.Type != proto.TRequest {
		t.Fatalf("read start request: %v (%v)", f, err)
	}
	var req proto.Request
	if err := json.Unmarshal(f.Payload, &req); err != nil || req.Op != "start" {
		t.Fatalf("expected start op, got %+v (%v)", req, err)
	}
	res, _ := json.Marshal(guest.StartResp{HolderPID: 100, AgentPID: 101, Cols: 80, Rows: 24})
	if err := w.WriteJSON(proto.TResponse, proto.Response{ID: req.ID, OK: true, Result: res}); err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			f, err := proto.Read(r)
			if err != nil {
				return
			}
			if f.Type != proto.TRequest {
				continue
			}
			var req proto.Request
			if json.Unmarshal(f.Payload, &req) != nil {
				continue
			}
			switch req.Op {
			case "spawn":
				res, _ := json.Marshal(guest.SpawnResp{PID: 202, Cols: 80, Rows: 24})
				_ = w.WriteJSON(proto.TResponse, proto.Response{ID: req.ID, OK: true, Result: res})
			case "stop":
				_ = w.WriteJSON(proto.TResponse, proto.Response{ID: req.ID, OK: true, Result: json.RawMessage(`{}`)})
				return
			}
		}
	}()
	return sess, w
}

func TestStopAndIdleStopUpdateChildSessionManifests(t *testing.T) {
	for _, tc := range []struct {
		name     string
		idleStop time.Duration
		sendStop bool
		wantSrc  string
	}{
		{name: "stop", sendStop: true, wantSrc: "user"},
		{name: "idle_stop", idleStop: 120 * time.Millisecond, wantSrc: "system"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hosttest.New(t)
			pID := "p" + tc.name[:3] + "2345"
			cID := "c" + tc.name[:3] + "6789"
			seedWalledManifest(t, pID, "shell")

			sb := newFakeSandbox(t)
			done := runSupervisorBG(t, pID, sb, supervisor.Options{
				HeartbeatTimeout: 5 * time.Second,
				TickInterval:     30 * time.Millisecond,
				StopGrace:        100 * time.Millisecond,
				IdleStop:         tc.idleStop,
				DiskUsageFunc:    func(string) (int64, int64, bool) { return 50 << 30, 100 << 30, true },
			})
			_, ctlW := startFakeGuestLinkWithSpawn(t, sb)

			spawnPayload := host.SpawnSocketRequest{
				Manifest: host.Manifest{
					Schema: 1, ID: cID, Name: "child-" + tc.name, SandboxOf: pID,
					CreatedAt: time.Now().UTC(),
					Agent:     host.AgentInfo{Kind: "shell", Argv: []string{"/bin/sh"}},
					Term:      host.TermInfo{Term: "xterm-256color", Cols: 80, Rows: 24},
				},
			}
			if _, err := host.HolderRequest(pID, "spawn", spawnPayload, 3*time.Second); err != nil {
				t.Fatalf("spawn child: %v", err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(paths.SessionDir(cID)) })

			// Populate Guest claims on the child while the primary is still working
			// so idle_stop cannot race ahead of the WaitFor assertion.
			_ = ctlW.WriteJSON(proto.TEvent, guest.Event{Type: "status", Session: cID, State: host.StateIdle, Source: "hook"})
			hosttest.WaitFor(t, 5*time.Second, "child status claim saved", func() bool {
				cm, err := host.LoadManifest(cID)
				return err == nil && cm.Guest != nil && cm.Status.State == host.StateIdle
			})
			_ = ctlW.WriteJSON(proto.TEvent, guest.Event{Type: "status", Session: pID, State: host.StateIdle, Source: "hook"})

			if tc.sendStop {
				if _, err := host.HolderRequest(pID, "stop", nil, 3*time.Second); err != nil {
					t.Fatalf("stop request: %v", err)
				}
			}

			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("RunWith returned error: %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatalf("supervisor did not exit after %s", tc.name)
			}

			for _, id := range []string{pID, cID} {
				m, err := host.LoadManifest(id)
				if err != nil {
					t.Fatalf("load manifest %s: %v", id, err)
				}
				if m.Status.State != host.StateStopped || m.Status.Source != tc.wantSrc || m.Status.Claimed {
					t.Fatalf("manifest %s status = %+v, want StateStopped source=%q claimed=false", id, m.Status, tc.wantSrc)
				}
				if m.Guest != nil || m.Run.HolderPID != 0 || m.Run.AgentPID != 0 || m.Run.Unit != "" || m.Run.RespawnCount != 0 {
					t.Fatalf("manifest %s kept runtime/guest fields: run=%+v guest=%+v", id, m.Run, m.Guest)
				}
				if _, err := os.Stat(paths.Socket(id)); !os.IsNotExist(err) {
					t.Errorf("socket for %s still present after %s: %v", id, tc.name, err)
				}
			}
		})
	}
}
