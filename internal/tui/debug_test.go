package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/bundle"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/diag"
)

// The launcher's own debug, and the bundle, in a test.

func (b *fakeBackend) DebugFile() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.debugFile
}

func (b *fakeBackend) StartDebug() (string, error) {
	b.record("debug on")
	b.mu.Lock()
	defer b.mu.Unlock()
	b.debugFile = "/state/debug/client-20261015-120000-1.jsonl"
	return b.debugFile, nil
}

func (b *fakeBackend) StopDebug() error {
	b.record("debug off")
	b.mu.Lock()
	defer b.mu.Unlock()
	b.debugFile = ""
	return nil
}

func (b *fakeBackend) SaveFlight() (string, error) {
	b.record("save the launcher")
	return "/state/debug/client-flight.trace", nil
}

// Bundle waits for bundleGate, when the test set one, or for the end of
// ctx, as a bundle that the launcher stops does.
func (b *fakeBackend) Bundle(ctx context.Context, h client.Host, session string) (*bundle.Result, error) {
	b.record("bundle %s %s", h.Name, session)
	if gate := b.bundleGate; gate != nil {
		b.blocked.Add(1)
		defer b.blocked.Add(-1)
		select {
		case <-gate:
		case <-ctx.Done():
			b.record("bundle stopped")
			return nil, ctx.Err()
		}
	}
	return &bundle.Result{Path: "/here/deep/in/a/folder/with/a/long/name/x-debug-20261015-120000.tar.gz", Size: 3 << 20}, nil
}

func (c *fakeConn) DebugStart(_ context.Context, id string, cats diag.Cats, d time.Duration) (client.DebugState, error) {
	c.be.record("debug start %s %s %s %s", c.name, id, cats, d)
	until := now.Add(d)
	return client.DebugState{Categories: cats.List(), Until: &until}, c.host().debugErr
}

func (c *fakeConn) DebugStop(_ context.Context, id string) (client.DebugState, error) {
	c.be.record("debug stop %s %s", c.name, id)
	return client.DebugState{Stopped: true}, nil
}

func (c *fakeConn) DebugSave(_ context.Context, id string) (client.DebugFileInfo, error) {
	c.be.record("debug save %s %s", c.name, id)
	return client.DebugFileInfo{Path: "~/.local/state/x/sessions/" + id + "/debug/flight.trace"}, nil
}

func (c *fakeConn) StartConnDebug(_ context.Context, cats diag.Cats) (client.DebugState, error) {
	c.be.record("conn debug %s %s", c.name, cats)
	return client.DebugState{}, c.host().debugErr
}

func (c *fakeConn) StopConnDebug(context.Context) error {
	c.be.record("conn debug stop %s", c.name)
	return nil
}

// runPalette runs a command from the palette, by its name, such as debug start.
func (hs *harness) runPalette(words string) {
	hs.t.Helper()
	hs.key("ctrl+o")
	hs.typeText(words)
	hs.key("enter")
}

// headLine is the launcher's header, as text.
func headLine(m *Model) string { return strings.Split(plainScreen(m), "\n")[headerY] }

func isDebugTick(msg tea.Msg) bool { _, ok := msg.(debugTickMsg); return ok }

// debugTimers are the waits of the pending debug ticks.
func (hs *harness) debugTimers() []time.Duration {
	var out []time.Duration
	for _, t := range hs.timers {
		if isDebugTick(t.msg) {
			out = append(out, t.d)
		}
	}
	return out
}

// debug: start turns on the selected session's debug, then the launcher's
// own, for 30 minutes. The header shows the minutes left, and repaints only
// when they change. debug: stop ends both, and the mark goes.
func TestDebugStartAndStopInTheLauncher(t *testing.T) {
	hs := sessions(t, 80, 24)
	if strings.Contains(headLine(hs.m), "debug") || len(hs.debugTimers()) != 0 {
		t.Fatalf("a mark or a tick with debug off: %s %v", headLine(hs.m), hs.debugTimers())
	}
	_, s := hs.m.selectedPair()
	hs.runPalette("debug start")
	calls := strings.Join(hs.be.callList(), "\n")
	if i, j := strings.Index(calls, "debug start atlas-dev "+s.ID+" attach,memory,runtime 30m0s"), strings.Index(calls, "debug on"); i < 0 || j < i {
		t.Fatalf("the launcher's debug did not start after the session's:\n%s", calls)
	}
	hasToast(t, hs.m, "Debug is on for the launcher, and for "+s.Name+" on atlas-dev until "+now.Add(30*time.Minute).Local().Format("15:04")+".")
	if !strings.Contains(headLine(hs.m), "debug 30m") {
		t.Fatalf("the header shows no mark: %s", headLine(hs.m))
	}
	if w := hs.debugTimers(); len(w) != 1 || w[0] != time.Minute {
		t.Fatalf("the mark's tick waits %v, want one of 1m", w)
	}
	hs.clock = hs.clock.Add(90 * time.Second)
	if hs.fire(isDebugTick) != 1 || !strings.Contains(headLine(hs.m), "debug 29m") {
		t.Fatalf("after 90 s: %s", headLine(hs.m))
	}
	if w := hs.debugTimers(); len(w) != 1 || w[0] != 30*time.Second {
		t.Fatalf("the next tick waits %v, want 30s: when the minutes change", w)
	}
	hs.runPalette("debug stop")
	hasCall(t, hs.be, "debug off")
	hasCall(t, hs.be, "debug stop atlas-dev "+s.ID)
	hasToast(t, hs.m, "Debug is off.")
	if strings.Contains(headLine(hs.m), "debug") {
		t.Fatalf("the mark stays after debug: stop: %s", headLine(hs.m))
	}
}

// When the host refuses debug: start, the launcher's own debug stays off.
func TestAFailedDebugStartLeavesNoMark(t *testing.T) {
	hs := sessions(t, 80, 24)
	hs.be.state["atlas-dev"].debugErr = &client.UserError{Msg: "the host helper on atlas-dev is too old for the debug mode", Hint: "update it"}
	hs.runPalette("debug start")
	if containsCall(hs.be, "debug on") || strings.Contains(headLine(hs.m), "debug") || len(hs.debugTimers()) != 0 {
		t.Fatalf("calls %v, header %s", hs.be.callList(), headLine(hs.m))
	}
	hasToast(t, hs.m, "The host helper on atlas-dev is too old for the debug mode.")
}

// With a host selected, debug: start turns on the debug of the connection
// to it. After 30 minutes the launcher's debug ends by itself, with that
// of the connection.
func TestTheLauncherDebugEndsByItself(t *testing.T) {
	hs := home(t, 80, 24)
	hs.runPalette("debug start")
	hasCall(t, hs.be, "conn debug atlas-dev attach,net,memory,launcher,runtime")
	hs.clock = hs.clock.Add(30 * time.Minute)
	for range 3 {
		hs.fire(isDebugTick)
	}
	hasCall(t, hs.be, "debug off")
	hasCall(t, hs.be, "conn debug stop atlas-dev")
	hasToast(t, hs.m, "Debug is off: its 30 minutes ended.")
	if strings.Contains(headLine(hs.m), "debug") {
		t.Fatalf("the mark stays: %s", headLine(hs.m))
	}
}

// --debug turned the launcher's debug on: the header shows the mark with
// no minutes, and a notice names the file, as no line comes before the
// launcher. debug: start on a host then ends the connection's debug after
// 30 minutes, with one tick, and leaves the launcher's own on.
func TestTheMarkOfDebugFromTheFlag(t *testing.T) {
	be := labBackend()
	be.debugFile = "/state/debug/client-1.jsonl"
	hs := newHarness(t, be, 60, 20)
	hasToast(t, hs.m, "Debug is on. The launcher writes to:")
	if !strings.Contains(plainScreen(hs.m), "/state/debug/client-1.jsonl") {
		t.Fatalf("the notice lost its path at 60 columns:\n%s", plainScreen(hs.m))
	}
	if h := headLine(hs.m); !strings.Contains(h, "debug") || strings.Contains(h, "debug 3") {
		t.Fatalf("header: %s", h)
	}
	hs.runPalette("debug save")
	hasCall(t, hs.be, "save the launcher")
	hs.runPalette("debug start")
	if w := hs.debugTimers(); len(w) != 1 || w[0] != 30*time.Minute {
		t.Fatalf("ticks %v, want one at the end", w)
	}
	hs.clock = hs.clock.Add(30 * time.Minute)
	hs.fire(isDebugTick)
	hasCall(t, hs.be, "conn debug stop atlas-dev")
	if containsCall(hs.be, "debug off") || hs.be.DebugFile() == "" {
		t.Fatal("the end of debug: start stopped the debug that --debug turned on")
	}
}

// debug: save saves the selected session's window. debug: bundle writes
// the bundle of the selected session, one at a time, and its notice keeps
// its path at 60 columns. Quitting stops a bundle that runs.
func TestDebugSaveAndBundleInTheLauncher(t *testing.T) {
	hs := sessions(t, 60, 20)
	_, s := hs.m.selectedPair()
	hs.runPalette("debug save")
	hasCall(t, hs.be, "debug save atlas-dev "+s.ID)
	if containsCall(hs.be, "save the launcher") {
		t.Fatal("the launcher saved a window with its debug off")
	}
	gate := make(chan struct{})
	hs.be.bundleGate = gate
	hs.runPalette("debug bundle")
	if !strings.Contains(headLine(hs.m), "bundling…") || !hs.m.glowing() {
		t.Fatalf("no sign of the bundle: %s", headLine(hs.m))
	}
	hs.runPalette("debug bundle")
	hasToast(t, hs.m, "A bundle is being written. Wait for it to end.")
	close(gate)
	hs.do(nil)
	hasCall(t, hs.be, "bundle atlas-dev "+s.ID)
	// The short notices before it have gone by now.
	hs.clock = hs.clock.Add(toastLife + time.Millisecond)
	hs.fire(func(msg tea.Msg) bool { _, ok := msg.(toastTickMsg); return ok })
	screen := plainScreen(hs.m)
	if !strings.Contains(screen, "Wrote the bundle. Read it before you share it.") || !strings.Contains(screen, "x-debug-20261015-120000.tar.gz · 3.0 MiB") {
		t.Fatalf("the notice at 60 columns:\n%s", screen)
	}
	if strings.Contains(headLine(hs.m), "bundling") {
		t.Fatalf("the mark stays: %s", headLine(hs.m))
	}
	hs.be.bundleGate = make(chan struct{})
	hs.runPalette("debug bundle")
	hs.m.Close()
	if !containsCall(hs.be, "bundle stopped") {
		t.Fatal("quitting did not stop the bundle")
	}
}

// At 60 columns, the mark and a round trip's spread fit the header, two
// cells apart at least.
func TestTheMarkFitsANarrowHeader(t *testing.T) {
	hs := unevenLab(t, 60, 20)
	hs.key("enter")
	hs.be.debugFile = "/state/debug/client-1.jsonl"
	hs.m.dbg.own, hs.m.dbg.until = true, hs.clock.Add(24*time.Minute)
	h := strings.TrimRight(headLine(hs.m), " ")
	if len([]rune(h)) > 60 || !strings.Contains(h, "debug") || !strings.Contains(h, "  1 needs you") && !strings.Contains(h, "ms") {
		t.Fatalf("header of %d cells: %q", len([]rune(h)), h)
	}
	if _, facts, _ := strings.Cut(h, "debug"); !strings.Contains(facts, "  ") {
		t.Fatalf("no two-cell gap: %q", h)
	}
}

func containsCall(be *fakeBackend, want string) bool {
	for _, c := range be.callList() {
		if c == want {
			return true
		}
	}
	return false
}
