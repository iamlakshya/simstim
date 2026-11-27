package tui

import (
	"context"
	"errors"
	"io"
	"os"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/client"
)

func TestLauncherRestartsInterruptedDoctorAfterAttachHandoff(t *testing.T) {
	be := newFakeBackend()
	be.hosts = []client.Host{{Name: "box", SSH: "box"}}
	s := client.Session{ID: "s1", Name: "auth-fix", Agent: "claude", State: "idle", Alive: true}
	be.state["box"] = &fakeHost{
		sessions: []client.Session{s},
		info:     client.HostInfo{Distro: "Ubuntu 24.04", Arch: "arm64", CPUs: 4},
	}
	be.attach = client.AttachResult{Detached: true}

	doctorEntered := make(chan struct{})
	var doctorCalls atomic.Int32
	var doctorBlocked atomic.Int64
	be.doctorHook = func(ctx context.Context, _ string) {
		if doctorCalls.Add(1) == 1 {
			doctorBlocked.Add(1)
			defer doctorBlocked.Add(-1)
			close(doctorEntered)
			<-ctx.Done()
		}
	}

	runs := 0
	var distroAfterResume string
	withFakeLauncherIO(t,
		func(*os.File) bool { return true },
		func(_ context.Context, pm tea.Model, _ io.Reader, _ io.Writer) error {
			runs++
			fr := pm.(*framed)
			switch runs {
			case 1:
				m := fr.m.(*Model)
				ps := newProgStepper(t, pm, m)
				ps.stepUntil(pm.Init(), doctorBlocked.Load)
				_, _ = pm.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
				<-doctorEntered
				if m.hostByName("box").info != nil {
					t.Fatal("doctor info populated while first Doctor was still blocked")
				}
				_, cmd := pm.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
				if cmd == nil {
					t.Fatal("Enter on session returned nil cmd")
				}
				ps.attachQuit(cmd, s.ID, doctorBlocked.Load)
			case 2:
				rm := fr.m.(*resumedModel)
				ps := newProgStepper(t, pm, rm.m)
				ps.step(pm.Init())
				if info := rm.m.hostByName("box").info; info != nil {
					distroAfterResume = info.Distro
				}
			default:
				t.Fatalf("unexpected run %d", runs)
			}
			return nil
		},
	)

	if err := runWith(context.Background(), be, Options{Now: func() time.Time { return now }}); err != nil {
		t.Fatalf("runWith = %v", err)
	}
	if doctorCalls.Load() < 2 {
		t.Fatalf("doctor calls = %d, want at least 2 (initial + resumed)", doctorCalls.Load())
	}
	if distroAfterResume != "Ubuntu 24.04" {
		t.Fatalf("host distro after resume = %q, want Ubuntu 24.04", distroAfterResume)
	}
}

// runWith checks all three streams (including stderr) and InputAllowed before
// creating a tea.Program.
func TestLauncherChecksStderrAndInputAllowed(t *testing.T) {
	be := newFakeBackend()
	runs := 0
	withFakeLauncherIO(t,
		func(f *os.File) bool { return f != os.Stderr },
		func(context.Context, tea.Model, io.Reader, io.Writer) error { runs++; return nil },
	)
	err := runWith(context.Background(), be, Options{})
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Code != "launcher_no_terminal" || ue.Kind != client.KindFailed || runs != 0 {
		t.Fatalf("redirected stderr: err=%v (code=%v), runs=%d", err, ue, runs)
	}

	withFakeLauncherIO(t,
		func(*os.File) bool { return true },
		func(context.Context, tea.Model, io.Reader, io.Writer) error { runs++; return nil },
	)
	err = runWith(client.WithoutInput(context.Background()), be, Options{})
	ue = nil
	if !errors.As(err, &ue) || ue.Code != "no_input" || ue.Kind != client.KindNeedsAnswer || runs != 0 {
		t.Fatalf("WithoutInput: err=%v (ue=%+v), runs=%d", err, ue, runs)
	}
}
