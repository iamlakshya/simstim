package tui

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/muesli/cancelreader"
	"golang.org/x/term"

	"github.com/iamlakshya/simstim/internal/client"
)

type noopCancelReader struct {
	once   sync.Once
	cancel chan struct{}
}

func newNoopCancelReader() *noopCancelReader {
	return &noopCancelReader{cancel: make(chan struct{})}
}

func (n *noopCancelReader) Read([]byte) (int, error) {
	<-n.cancel
	return 0, cancelreader.ErrCanceled
}

func (n *noopCancelReader) Cancel() bool {
	n.once.Do(func() { close(n.cancel) })
	return true
}

func (n *noopCancelReader) Close() error {
	n.Cancel()
	return nil
}

func withFakeLauncherIO(t *testing.T, termFn func(*os.File) bool, progFn func(context.Context, tea.Model, io.Reader, io.Writer) error) string {
	t.Helper()
	errPath := filepath.Join(t.TempDir(), "stderr.txt")
	errFile, err := os.Create(errPath)
	if err != nil {
		t.Fatal(err)
	}
	oldErr := os.Stderr
	oldTerm, oldProg, oldState := isTerminal, runProgram, launcherState
	launcherState = func(int) (*term.State, error) { return nil, nil }
	restoreReader := SetLauncherReader(func(io.Reader) (cancelreader.CancelReader, error) {
		return newNoopCancelReader(), nil
	})
	os.Stderr = errFile
	isTerminal = termFn
	runProgram = progFn
	t.Cleanup(func() {
		restoreReader()
		os.Stderr = oldErr
		isTerminal, runProgram, launcherState = oldTerm, oldProg, oldState
		_ = errFile.Close()
	})
	return errPath
}

// When be.Attach returns ReaderStuck = true in the runWith handoff loop, the
// launcher writes the detach notice to stderr and exits cleanly without
// starting a second tea.Program or terminal reader.
func TestLauncherStopsCleanlyWhenAttachReaderStuck(t *testing.T) {
	be := newFakeBackend()
	be.hosts = []client.Host{{Name: "box", SSH: "box"}}
	s := client.Session{ID: "s1", Name: "auth-fix", Agent: "claude", State: "idle", Alive: true}
	be.state["box"] = &fakeHost{sessions: []client.Session{s}}
	be.attach = client.AttachResult{Detached: true, ReaderStuck: true}

	runs := 0
	errPath := withFakeLauncherIO(t,
		func(*os.File) bool { return true },
		func(_ context.Context, pm tea.Model, _ io.Reader, _ io.Writer) error {
			runs++
			if runs > 1 {
				t.Fatal("runWith started a second tea.Program while the attach reader was stuck")
			}
			m := pm.(*framed).m.(*Model)
			ps := newProgStepper(t, pm, m)
			_, _ = pm.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
			_, _ = pm.Update(sessionsMsg{name: "box", gen: 0, list: []client.Session{s}})
			_, cmd := pm.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			if cmd == nil {
				t.Fatal("Enter on session returned nil cmd")
			}
			ps.attachQuit(cmd, "box", s.ID, nil)
			return nil
		},
	)

	if err := runWith(context.Background(), be, Options{Now: func() time.Time { return now }}); err != nil {
		t.Fatalf("runWith = %v", err)
	}
	if runs != 1 {
		t.Fatalf("ran %d programs, want 1", runs)
	}
	if len(be.attached) != 1 || be.attached[0].Session != "s1" {
		t.Fatalf("attached = %+v", be.attached)
	}
	got, err := os.ReadFile(errPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "Detached. auth-fix keeps running on box.") {
		t.Errorf("stderr = %q, want detach notice", got)
	}
}

// When be.Attach returns ReaderStuck = false, runWith resumes the launcher in
// a fresh tea.Program whose Init() executes without re-dialling already-online
// hosts.
func TestLauncherResumesAfterCleanAttachHandoff(t *testing.T) {
	be := newFakeBackend()
	be.hosts = []client.Host{{Name: "box", SSH: "box"}}
	s := client.Session{ID: "s1", Name: "auth-fix", Agent: "claude", State: "idle", Alive: true}
	be.state["box"] = &fakeHost{
		sessions: []client.Session{s},
		info:     client.HostInfo{Distro: "Ubuntu 24.04", Arch: "arm64", CPUs: 4},
	}
	be.attach = client.AttachResult{Detached: true, ReaderStuck: false}

	runs := 0
	var resumedToast string
	var connCount int
	withFakeLauncherIO(t,
		func(*os.File) bool { return true },
		func(_ context.Context, pm tea.Model, _ io.Reader, _ io.Writer) error {
			runs++
			fr := pm.(*framed)
			switch runs {
			case 1:
				m := fr.m.(*Model)
				ps := newProgStepper(t, pm, m)
				_, _ = pm.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
				ps.step(pm.Init())
				_, cmd := pm.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
				if cmd == nil {
					t.Fatal("Enter on session returned nil cmd")
				}
				ps.attachQuit(cmd, "box", s.ID, nil)
			case 2:
				rm, ok := fr.m.(*resumedModel)
				if !ok {
					t.Fatalf("second run wrapped %T, want *resumedModel", fr.m)
				}
				ps := newProgStepper(t, pm, rm.m)
				ps.step(pm.Init())
				connCount = rm.m.conns.count()
				if len(rm.m.toasts.shown) > 0 {
					resumedToast = rm.m.toasts.shown[0].text
				}
			default:
				t.Fatalf("unexpected program run %d", runs)
			}
			return nil
		},
	)

	if err := runWith(context.Background(), be, Options{Now: func() time.Time { return now }}); err != nil {
		t.Fatalf("runWith = %v", err)
	}
	if runs != 2 {
		t.Fatalf("ran %d programs, want 2", runs)
	}
	if !strings.Contains(resumedToast, "Detached. auth-fix keeps running on box.") {
		t.Errorf("resumed toast = %q, want detach toast", resumedToast)
	}
	if be.dials["box"] != 1 {
		t.Errorf("box dialled %d times, want 1 (kept existing online connection)", be.dials["box"])
	}
	if connCount != 1 {
		t.Errorf("active conns on resume = %d, want 1", connCount)
	}
}

// An in-flight dial whose result arrives after the program quits for attach
// (never adopted by Update) is closed on handoff and leaves no leaked Conn in
// m.conns; resumeAfterAttach then dials the host cleanly.
func TestLauncherDropsUnadoptedDialAcrossAttachHandoff(t *testing.T) {
	be := newFakeBackend()
	be.hosts = []client.Host{{Name: "box", SSH: "box"}, {Name: "slow", SSH: "slow"}}
	s := client.Session{ID: "s1", Name: "auth-fix", Agent: "claude", State: "idle", Alive: true}
	be.state["box"] = &fakeHost{sessions: []client.Session{s}}
	be.state["slow"] = &fakeHost{}
	be.attach = client.AttachResult{Detached: true}

	slowEntered := make(chan struct{})
	slowRelease := make(chan struct{})
	var slowCalls atomic.Int32
	var slowBlocked atomic.Int64
	be.dialHook = func(_ context.Context, h client.Host) {
		if h.Name != "slow" {
			return
		}
		if slowCalls.Add(1) == 1 {
			slowBlocked.Add(1)
			defer slowBlocked.Add(-1)
			close(slowEntered)
			<-slowRelease
		}
	}

	var modelRef *Model
	var slowFirst *fakeConn
	var connsDuringAttach int
	var slowClosedDuringAttach bool
	be.attachHook = func(client.AttachOptions) {
		connsDuringAttach = modelRef.conns.count()
		if slowFirst != nil {
			slowClosedDuringAttach = slowFirst.isClosed()
		}
	}

	runs := 0
	var connsAfterResume int
	var slowOnlineAfterResume bool
	withFakeLauncherIO(t,
		func(*os.File) bool { return true },
		func(_ context.Context, pm tea.Model, _ io.Reader, _ io.Writer) error {
			runs++
			fr := pm.(*framed)
			switch runs {
			case 1:
				m := fr.m.(*Model)
				modelRef = m
				ps := newProgStepper(t, pm, m)
				_, _ = pm.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
				ps.stepUntil(pm.Init(), slowBlocked.Load)
				<-slowEntered

				// Navigate into box's sessions and trigger attach handoff.
				_, cmd := pm.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
				ps.stepUntil(cmd, slowBlocked.Load)
				_, cmd = pm.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
				if cmd == nil {
					t.Fatal("Enter on session returned nil cmd")
				}
				ps.attachQuit(cmd, "box", s.ID, slowBlocked.Load)

				// Let slow's Dial finish and call addUnadopted right as the
				// program exits, without delivering dialledMsg to Update.
				close(slowRelease)
				for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
					if m.conns.count() == 2 {
						break
					}
					if time.Now().After(deadline) {
						t.Fatalf("slow dial did not reach addUnadopted; count=%d", m.conns.count())
					}
				}
				slowFirst = be.conn("slow")
			case 2:
				rm := fr.m.(*resumedModel)
				ps := newProgStepper(t, pm, rm.m)
				ps.step(pm.Init())
				connsAfterResume = rm.m.conns.count()
				if h := rm.m.hostByName("slow"); h != nil {
					slowOnlineAfterResume = h.online()
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
	if !slowClosedDuringAttach {
		t.Fatal("unadopted slow connection was not closed before Attach")
	}
	if connsDuringAttach != 1 {
		t.Fatalf("m.conns during Attach = %d, want 1 (only adopted box)", connsDuringAttach)
	}
	if !slowOnlineAfterResume || connsAfterResume != 2 {
		t.Fatalf("after resume: slowOnline=%v conns=%d, want true and 2", slowOnlineAfterResume, connsAfterResume)
	}
}

// Across repeated attach/detach cycles, waitDone never accumulates duplicate
// watcher goroutines on the same connection, and a connection closing during
// Attach still delivers connDoneMsg immediately on resume.
func TestLauncherWaitDoneDoesNotAccumulateAndHandlesCloseDuringAttach(t *testing.T) {
	const cycles = 5
	be := newFakeBackend()
	be.hosts = []client.Host{{Name: "box", SSH: "box"}}
	s := client.Session{ID: "s1", Name: "auth-fix", Agent: "claude", State: "idle", Alive: true}
	be.state["box"] = &fakeHost{sessions: []client.Session{s}}
	be.attach = client.AttachResult{Detached: true}

	var modelRef *Model
	attachNum := 0
	be.attachHook = func(client.AttachOptions) {
		attachNum++
		if w := modelRef.waiters.Load(); w != 0 {
			t.Errorf("attach %d: waiters during Attach = %d, want 0", attachNum, w)
		}
		if attachNum == cycles {
			// Close the connection while attached on the final cycle.
			// attachHook runs with be.mu already held.
			if n := len(be.conns); n > 0 {
				be.conns[n-1].Close()
			}
		}
	}

	runs := 0
	var statusAfterClose connStatus
	withFakeLauncherIO(t,
		func(*os.File) bool { return true },
		func(_ context.Context, pm tea.Model, _ io.Reader, _ io.Writer) error {
			runs++
			fr := pm.(*framed)
			var m *Model
			if runs == 1 {
				m = fr.m.(*Model)
				modelRef = m
			} else {
				m = fr.m.(*resumedModel).m
			}
			ps := newProgStepper(t, pm, m)
			ps.step(pm.Init())
			_, _ = pm.Update(tea.WindowSizeMsg{Width: 100, Height: 30})

			if runs <= cycles {
				if w := m.waiters.Load(); w != 1 {
					t.Fatalf("run %d: active waitDone goroutines = %d, want 1", runs, w)
				}
				_, cmd := pm.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
				if cmd == nil {
					t.Fatalf("run %d: Enter returned nil cmd", runs)
				}
				ps.attachQuit(cmd, "box", s.ID, nil)
				return nil
			}
			statusAfterClose = m.hostByName("box").status
			return nil
		},
	)

	if err := runWith(context.Background(), be, Options{Now: func() time.Time { return now }}); err != nil {
		t.Fatalf("runWith = %v", err)
	}
	if runs != cycles+1 {
		t.Fatalf("ran %d programs, want %d", runs, cycles+1)
	}
	if statusAfterClose == stOnline {
		t.Fatalf("box stayed stOnline after its connection closed during Attach")
	}
}

// When Doctor is still in flight when the launcher hands off to Attach,
// pauseBg cancels it and resumeAfterAttach re-issues Doctor so host metadata is
// not permanently lost.
