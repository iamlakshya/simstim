package supervisor

import (
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/host"
)

func TestFinalizeChildrenLockedMirrorsStopKinds(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)

	newSupWithChild := func(childState string) (*Supervisor, *sandboxSession) {
		pm := &host.Manifest{
			Schema: 1, ID: "prim1111", Name: "primary",
			Run:    host.Runtime{HolderPID: 100, AgentPID: 101, Unit: "u1", RespawnCount: 2},
			Status: host.Status{State: host.StateWorking, Since: now, Claimed: true},
			Guest:  &host.GuestClaims{State: host.StateWorking},
		}
		cm := &host.Manifest{
			Schema: 1, ID: "chld2222", Name: "child", SandboxOf: "prim1111",
			Run:    host.Runtime{HolderPID: 100, AgentPID: 202, Unit: "u1", RespawnCount: 1},
			Status: host.Status{State: childState, Since: now, Claimed: true},
			Guest:  &host.GuestClaims{State: childState},
		}
		child := &sandboxSession{id: cm.ID, m: cm}
		s := &Supervisor{
			id: pm.ID,
			m:  pm,
			sessions: map[string]*sandboxSession{
				pm.ID: {id: pm.ID, m: pm},
				cm.ID: child,
			},
		}
		return s, child
	}

	for _, tc := range []struct {
		name         string
		sr           stopRequest
		initialState string
		wantState    string
		wantSource   string
		wantCleared  bool
	}{
		{
			name:         "stop",
			sr:           stopRequest{kind: stopKindStop, reason: "stopped by request"},
			initialState: host.StateWorking,
			wantState:    host.StateStopped,
			wantSource:   "user",
			wantCleared:  true,
		},
		{
			name:         "idle_stop",
			sr:           stopRequest{kind: stopKindIdleStop, reason: "idle for 30m", system: true},
			initialState: host.StateIdle,
			wantState:    host.StateStopped,
			wantSource:   "system",
			wantCleared:  true,
		},
		{
			name:         "disk_full",
			sr:           stopRequest{kind: stopKindDiskFull, reason: "disk_full: low space", system: true},
			initialState: host.StateWorking,
			wantState:    host.StateStopped,
			wantSource:   "system",
			wantCleared:  true,
		},
		{
			name:         "host_stop preserves active state",
			sr:           stopRequest{kind: stopKindHostStop, reason: "stopped with the host", system: true},
			initialState: host.StateWorking,
			wantState:    host.StateWorking,
			wantSource:   "system",
			wantCleared:  true,
		},
		{
			name:         "kill",
			sr:           stopRequest{kind: stopKindKill, reason: "killed by request"},
			initialState: host.StateWorking,
			wantState:    host.StateExited,
			wantSource:   "",
			wantCleared:  false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, child := newSupWithChild(tc.initialState)
			copies := s.finalizeChildrenLocked(tc.sr, now)
			if len(copies) != 1 {
				t.Fatalf("copies len = %d, want 1", len(copies))
			}
			if !child.ended {
				t.Fatal("child.ended = false, want true")
			}
			got := copies[0]
			if got.Status.State != tc.wantState || got.Status.Source != tc.wantSource || got.Status.Reason != tc.sr.reason || got.Status.Claimed {
				t.Fatalf("child status = %+v, want state=%s source=%q reason=%q claimed=false", got.Status, tc.wantState, tc.wantSource, tc.sr.reason)
			}
			if tc.wantCleared {
				if got.Guest != nil || got.Run.HolderPID != 0 || got.Run.AgentPID != 0 || got.Run.Unit != "" {
					t.Fatalf("child runtime/guest not cleared: run=%+v guest=%+v", got.Run, got.Guest)
				}
			}
		})
	}
}
