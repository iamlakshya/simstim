package tui

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/proto"
)

// gib15 is a memory cap of 1.5 GiB, which a host with 4 GB of memory gets.
const gib15 = 3 << 29

// staleMark is the text a holder writes when the cap stops a process other
// than the agent. An older holder writes it with the out-of-memory mark.
const staleMark = api.ReasonChildOOM

// endedLab is the lab with three agents on gcp-dev that have ended: the
// memory cap stopped one, a signal killed one, and one was lost in a host
// restart. The first two holders still answer, as a holder does after its
// agent exits. It opens gcp-dev's sessions, with the one the memory cap
// stopped selected.
func endedLab(t *testing.T, w, h int) *harness {
	t.Helper()
	be := labBackend()
	g := be.state["gcp-dev"]
	g.sessions = append(g.sessions,
		client.Session{ID: "g3", Name: "importer", Agent: "claude", Cwd: "/srv/app", State: api.StateErrored, Source: "process",
			Reason: "oom", OOM: true, Signal: "killed", ExitCode: code(137), Since: ago(time.Hour), CreatedAt: ago(90 * time.Minute),
			Alive: true, MemoryCurrent: 942 << 20, MemoryMax: gib15},
		client.Session{ID: "g4", Name: "fuzz", Agent: "shell", Cwd: "/srv/fuzz", State: api.StateErrored, Source: "process",
			Signal: "killed", ExitCode: code(137), Since: ago(20 * time.Minute), CreatedAt: ago(3 * time.Hour),
			Alive: true, MemoryCurrent: 12 << 20, MemoryMax: gib15},
		client.Session{ID: "g5", Name: "pairing", Agent: "codex", Cwd: "/srv/pair", State: api.StateLost, Reason: "the host restarted",
			Since: ago(2 * time.Hour), CreatedAt: ago(4 * time.Hour)},
	)
	hs := newHarness(t, be, w, h)
	hs.m.motion = false
	hs.key("down", "down", "enter", "down")
	// What the agent's screen said last, before it ended.
	hs.m.activity[sessKey{"gcp-dev", "g3"}] = activity{text: "⎿ Tip: Use /btw to ask a quick side question"}
	return hs
}

// The label comes from how the agent ended, not from the state alone: the
// host marks every non-zero exit "errored", and writes Go's name for a
// signal. Every label fits in the state column.
func TestLookSaysHowTheAgentEnded(t *testing.T) {
	m := sessions(t, 80, 24).m
	errored := func(sig string, c int) client.Session {
		return client.Session{State: api.StateErrored, Signal: sig, ExitCode: code(c), Alive: true}
	}
	cases := []struct {
		name              string
		s                 client.Session
		glyph, label, why string
	}{
		{"memory cap", client.Session{State: api.StateErrored, Reason: "oom", OOM: true, Signal: "killed", ExitCode: code(137), MemoryMax: gib15},
			glyphFail, "out of memory", "stopped at its memory cap of 1.5 GiB"},
		{"memory cap, older name", client.Session{State: api.StateErrored, OOM: true, Signal: "SIGKILL"}, glyphFail, "out of memory", "stopped at its memory cap"},
		// An older holder marks the session again after its agent exits, when
		// the cap stopped another process earlier. Without SIGKILL, the mark
		// is not how the agent ended.
		{"stale mark, exit 0", client.Session{State: api.StateExited, ExitCode: code(0), OOM: true, Reason: staleMark}, glyphOK, "exited 0", "exited with code 0"},
		{"stale mark, exit 1", client.Session{State: api.StateErrored, ExitCode: code(1), OOM: true, Reason: staleMark}, glyphFail, "exited 1", "exited with code 1"},
		// A newer holder gives the text, not the mark, when the cap stopped
		// another process just before the agent exited.
		{"child stopped, exit 1", client.Session{State: api.StateErrored, Source: "process", ExitCode: code(1), Reason: staleMark},
			glyphFail, "exited 1", "exited with code 1"},
		{"stale mark, term", client.Session{State: api.StateErrored, Signal: "terminated", ExitCode: code(143), OOM: true},
			glyphFail, "terminated", "stopped by signal 15 (terminated)"},
		{"kill", errored("killed", 137), glyphFail, "killed", "stopped by signal 9 (killed)"},
		{"kill, older name", errored("SIGKILL", 137), glyphFail, "killed", "stopped by signal 9 (SIGKILL)"},
		{"term", errored("terminated", 143), glyphFail, "terminated", "stopped by signal 15 (terminated)"},
		{"int", errored("interrupt", 130), glyphFail, "interrupted", "stopped by signal 2 (interrupt)"},
		{"hup", errored("hangup", 129), glyphFail, "hung up", "stopped by signal 1 (hangup)"},
		{"segv", errored("segmentation fault", 139), glyphFail, "crashed", "stopped by signal 11 (segmentation fault)"},
		{"other signal", errored("user defined signal 1", 138), glyphFail, "stopped", "stopped by signal 10 (user defined signal 1)"},
		{"signal, no code", client.Session{State: api.StateErrored, Signal: "killed"}, glyphFail, "killed", "stopped by a signal (killed)"},
		{"exit code", errored("", 2), glyphFail, "exited 2", "exited with code 2"},
		{"did not start", client.Session{State: api.StateErrored, ExitCode: code(127), Reason: "exec: claude: not found"},
			glyphFail, "exited 127", "exited with code 127: exec: claude: not found"},
		{"widest code", errored("", 255), glyphFail, "exited 255", "exited with code 255"},
		{"done", client.Session{State: api.StateExited, ExitCode: code(0)}, glyphOK, "exited 0", "exited with code 0"},
		{"no code", client.Session{State: api.StateErrored}, glyphFail, "failed", "ended"},
		{"stopped by request", client.Session{State: api.StateExited, Reason: "killed by request"}, glyphOK, "exited", "killed by request"},
		{"lost", client.Session{State: api.StateLost, Reason: "the host restarted", OOM: true}, glyphOffline, "lost", "the host restarted"},
	}
	for _, c := range cases {
		if !ended(c.s) {
			t.Errorf("%s: not ended", c.name)
		}
		glyph, label := m.look(c.s)
		if glyph.s != c.glyph || label.s != c.label {
			t.Errorf("%s: look = %q %q, want %q %q", c.name, glyph.s, label.s, c.glyph, c.label)
		}
		if n := cells(label.s); n > colState {
			t.Errorf("%s: %q is %d cells, wider than the state column (%d)", c.name, label.s, n, colState)
		}
		if got := endWhy(c.s); got != c.why {
			t.Errorf("%s: why = %q, want %q", c.name, got, c.why)
		}
	}
	for _, state := range []string{api.StateStarting, api.StateWorking, api.StateIdle, api.StateNeedsInput} {
		s := client.Session{State: state, OOM: true, Signal: "killed", Alive: true}
		if ended(s) {
			t.Errorf("%s with a stopped process counts as ended", state)
		}
		if _, label := m.look(s); cells(label.s) > colState || label.s == "out of memory" {
			t.Errorf("%s with a stopped process looks like %q", state, label.s)
		}
	}
}

// The trace is coloured like the state: cyan for working, yellow for needs
// input, and faint otherwise, also for an agent that failed.
func TestTraceIsFaintUnlessWorkingOrWaiting(t *testing.T) {
	cases := map[string]lipgloss.Style{
		api.StateWorking: sKey, api.StateNeedsInput: sWarning, api.StateStarting: sFaint,
		api.StateIdle: sFaint, api.StateExited: sFaint, api.StateErrored: sFaint, api.StateLost: sFaint,
	}
	for state, want := range cases {
		got := traceStyle(client.Session{State: state, OOM: true})
		if got.GetForeground() != want.GetForeground() || got.GetFaint() != want.GetFaint() {
			t.Errorf("the trace of a %s session is not drawn like %v", state, want)
		}
	}
	// As drawn: the row of the killed agent, not selected, has a faint
	// trace, though its state is red.
	hs := endedLab(t, 80, 24)
	flat := strings.Repeat("⣀", traceCells)
	for i, r := range hs.m.rows() {
		if r.s.ID != "g4" {
			continue
		}
		row := hs.m.sessRowView(r, i+1, sessCols(74), false, false)
		if !strings.Contains(row, sDanger.Render(fit("killed", colState))) || !strings.HasSuffix(row, sFaint.Render(flat)) {
			t.Errorf("the killed agent's row is not drawn with a red state and a faint trace: %q", row)
		}
		return
	}
	t.Fatal("no row for the killed agent")
}

// An agent that the memory cap stopped: the row shows the whole state, and
// the line under the list says why it ended and what to do next, instead
// of its last line and the memory its session still holds. Enter goes to
// its last screen, and nothing offers to attach to it.
func TestEndedSessionSaysWhyAndWhatNext(t *testing.T) {
	hs := endedLab(t, 80, 24)
	if s := hs.m.selected(); s == nil || s.ID != "g3" {
		t.Fatalf("selected %v, want the importer", s)
	}
	out := plainScreen(hs.m)
	for _, want := range []string{
		"2 ✕ importer",
		"out of memory  ",
		"✕ stopped at its memory cap of 1.5 GiB   d delete",
		"d delete   enter peek   r rename",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q on the screen:\n%s", want, out)
		}
	}
	for _, not := range []string{"942 MiB", "Tip:", "attach", "out of memo…"} {
		if strings.Contains(out, not) {
			t.Errorf("%q on the screen:\n%s", not, out)
		}
	}
	if hs.m.readActivity() != nil {
		t.Error("the launcher reads the screen of an agent that has ended")
	}

	// Enter, and the row's number, go to the last screen.
	for _, k := range []string{"enter", "2"} {
		hs.key(k)
		if !hs.m.previewOpen() || hs.m.preview.key.id != "g3" {
			t.Fatalf("%s opened %T, not the last screen", k, hs.m.modal)
		}
		out := plainScreen(hs.m)
		if strings.Contains(out, "enter attach") || strings.Contains(out, "942 MiB") {
			t.Errorf("the peek offers to attach, or shows the memory:\n%s", out)
		}
		hs.key("enter")
		if !hs.m.previewOpen() {
			t.Error("Enter in the peek of an agent that has ended closed it")
		}
		hs.key("esc")
	}
	if len(hs.be.attached) != 0 {
		t.Fatalf("attached to an agent that has ended: %+v", hs.be.attached)
	}
	for _, c := range commands {
		if n := c.name(); (n == "session: attach" || n == "session: copy attach command") && c.reason(hs.m) != agentEnded {
			t.Errorf("the palette offers %q: %q", n, c.reason(hs.m))
		}
	}
	hs.key("y")
	hasToast(t, hs.m, "Not available: the agent has ended.")

	hs.key("d")
	c, ok := hs.m.modal.(*confirm)
	if !ok {
		t.Fatalf("d opened %T", hs.m.modal)
	}
	if text := strings.Join(c.lines, " "); strings.Contains(text, "stops") || strings.Contains(text, "Unsaved") {
		t.Errorf("the delete sheet warns about work in an agent that has ended: %q", text)
	}
	hs.key("esc", "down")
	if out := plainScreen(hs.m); !strings.Contains(out, "killed  ") || !strings.Contains(out, "✕ stopped by signal 9 (killed)   d delete") {
		t.Errorf("the killed session does not say why it ended:\n%s", out)
	}
}

// A lost agent can come back with a restore, which needs its ID: the line
// offers the restore first, and Enter says what to try, since there is no
// screen to show. The command may take the room of the folder and the ID,
// and a later step shows when it fits. The keys and the palette agree that
// there is no peek.
func TestLostSessionOffersRestore(t *testing.T) {
	for _, c := range []struct {
		w     int
		after string // what follows the command on the line
	}{{80, "   /srv/pair"}, {120, "   d delete"}} {
		hs := endedLab(t, c.w, 36)
		hs.key("down", "down")
		if s := hs.m.selected(); s == nil || s.ID != "g5" {
			t.Fatalf("selected %v, want the lost session", s)
		}
		restore := restoreCommand(hs.m.hostByName("gcp-dev"))
		out := plainScreen(hs.m)
		line := strings.Split(out, "\n")[hs.m.geo.detailY]
		if !strings.Contains(line, "◌ the host restarted   → "+restore+c.after) || cells(line) > c.w {
			t.Errorf("at %d columns the line is %q", c.w, line)
		}
		if !strings.Contains(out, "lost  ") || !strings.Contains(out, "enter how to restore   d delete   r rename") {
			t.Errorf("at %d columns the keys do not offer the restore:\n%s", c.w, out)
		}
		if strings.Contains(out, "peek") {
			t.Errorf("the keys offer a peek without a holder:\n%s", out)
		}
		for _, cmd := range commands {
			if cmd.name() == "session: preview" && cmd.reason(hs.m) == "" {
				t.Error("the palette offers a peek without a holder")
			}
		}
		hs.key("enter")
		if hs.m.modal != nil || len(hs.be.attached) != 0 {
			t.Fatalf("Enter on a lost session opened %T", hs.m.modal)
		}
		if ts := hs.m.toasts.shown; len(ts) == 0 || ts[len(ts)-1].text != "pairing on gcp-dev ended (the host restarted)." || ts[len(ts)-1].hint != restore {
			t.Errorf("Enter did not say what to try: %+v", ts)
		}
	}
}

// Enter on an agent whose holder has gone too gives the notice that ends
// an attach, with the same step after the memory cap. A session that does
// not say how its agent ended gets no empty cause.
func TestEnterWithoutAHolder(t *testing.T) {
	hs := endedLab(t, 80, 24)
	h := hs.m.hostByName("gcp-dev")
	for i := range h.sessions {
		switch h.sessions[i].ID {
		case "g3":
			h.sessions[i].Alive = false
		case "g4":
			h.sessions[i] = client.Session{ID: "g4", Name: "fuzz", Agent: "shell", Cwd: "/srv/fuzz", State: api.StateExited,
				Reason: "killed by request", Since: ago(time.Hour), CreatedAt: ago(3 * time.Hour)}
		}
	}
	last := func() toast {
		ts := hs.m.toasts.shown
		if len(ts) == 0 {
			t.Fatal("no notice")
		}
		return ts[len(ts)-1]
	}
	hs.key("enter")
	if n := last(); hs.m.modal != nil || n.text != "importer on gcp-dev ended (out of memory)." || n.hint != client.EndHint(&proto.Ended{OOM: true}) {
		t.Errorf("Enter gave %T and %+v", hs.m.modal, n)
	}
	hs.key("down", "enter")
	if n := last(); n.text != "fuzz on gcp-dev ended, and its holder has stopped." || !strings.Contains(n.hint, "Press d") {
		t.Errorf("Enter gave %+v", n)
	}
}

// When the cap stopped another process just before the agent exited, the
// line keeps that at the right: a newer holder gives the reason, and an
// older one the mark without the agent's own SIGKILL.
func TestEndedSessionNotesAStoppedProcess(t *testing.T) {
	for _, c := range []struct {
		name string
		s    client.Session
		why  string
	}{
		{"newer holder", client.Session{State: api.StateErrored, Source: "process", ExitCode: code(1), Reason: api.ReasonChildOOM}, "✕ exited with code 1   d delete"},
		{"older holder", client.Session{State: api.StateExited, Source: "process", ExitCode: code(0), OOM: true, Reason: staleMark}, "✓ exited with code 0   d delete"},
	} {
		hs := endedLab(t, 80, 24)
		h := hs.m.hostByName("gcp-dev")
		for i := range h.sessions {
			if h.sessions[i].ID == "g3" {
				s := c.s
				s.ID, s.Name, s.Agent, s.Cwd, s.Alive, s.Since, s.CreatedAt = "g3", "importer", "claude", "/srv/app", true, ago(time.Hour), ago(90*time.Minute)
				h.sessions[i] = s
			}
		}
		hs.m.syncSel()
		line := strings.Split(plainScreen(hs.m), "\n")[hs.m.geo.detailY]
		if !strings.Contains(line, c.why) || !strings.Contains(line, "/srv/app · "+capNote+" · g3") || strings.Contains(line, "ran out of memory") {
			t.Errorf("%s: the line is %q", c.name, line)
		}
	}
}

// The line under the list gives the reason room before the next steps,
// stays within the width, and leaves the steps out of search, where d types
// a letter.
func TestEndedLineFitsAndKeepsKeysTrue(t *testing.T) {
	for _, w := range []int{60, 80, 120} {
		hs := endedLab(t, w, 24)
		for _, k := range []string{"", "down", "down"} {
			if k != "" {
				hs.key(k)
			}
			line := strings.Split(plainScreen(hs.m), "\n")[hs.m.geo.detailY]
			if cells(line) > w {
				t.Errorf("at %d columns the line is %d cells: %q", w, cells(line), line)
			}
			if !strings.Contains(line, "✕ stopped") && !strings.Contains(line, "◌ the host restarted") {
				t.Errorf("at %d columns the line does not say why: %q", w, line)
			}
		}
	}
	hs := endedLab(t, 120, 24)
	hs.key("/")
	hs.typeText("importer")
	out := plainScreen(hs.m)
	if !strings.Contains(out, "✕ stopped at its memory cap of 1.5 GiB") || strings.Contains(out, "d delete") ||
		strings.Contains(out, "enter attach") || !strings.Contains(out, "enter peek") {
		t.Errorf("search offers keys that do not work there:\n%s", out)
	}
}

// When the memory cap stops another process while the agent runs on, the
// session keeps its live state, and the left of the line keeps what the
// agent asks or does. A short note at the right says what the cap did, when
// there is room for it.
func TestRunningSessionNotesAStoppedProcess(t *testing.T) {
	for _, w := range []int{60, 80, 120} {
		be := labBackend()
		s := &be.state["atlas-dev"].sessions[2] // api-tests, working
		s.OOM, s.Reason, s.MemoryCurrent, s.MemoryMax = true, staleMark, 1<<30, gib15
		hs := newHarness(t, be, w, 24)
		hs.m.motion = false
		hs.key("enter", "down")
		// A question that a screen rule found: the session has no reason.
		hs.m.activity[sessKey{"atlas-dev", "s3"}] = activity{text: "Allow command?"}
		out := plainScreen(hs.m)
		line := strings.Split(out, "\n")[hs.m.geo.detailY]
		if !strings.Contains(out, "working  ") || !strings.Contains(out, "enter attach") || !strings.Contains(line, "▸ Allow command?") {
			t.Errorf("at %d columns the session does not keep its live state:\n%s", w, out)
		}
		if cells(line) > w {
			t.Errorf("at %d columns the line is %d cells: %q", w, cells(line), line)
		}
		note := strings.Contains(hs.m.sessionDetail(hs.m.hostByName("atlas-dev"), *hs.m.selected(), hs.m.cw()-2, false), sWarning.Render(capNote))
		if want := w > 60; note != want || note && !strings.Contains(line, "~/src/api · 1 of 1.5 GiB · "+capNote) {
			t.Errorf("at %d columns the note shows: %v, want %v: %q", w, note, want, line)
		}
		hs.key("enter")
		if len(hs.be.attached) != 1 {
			t.Fatal("Enter did not attach to a session whose agent runs")
		}
	}
}

// The cap note never cuts the agent's question: it shows only when the
// question still fits whole, and the line is then as it would be without
// the mark.
func TestCapNoteKeepsTheQuestionWhole(t *testing.T) {
	const question = "Claude needs your permission to use Bash: npm test --coverage"
	line := func(w int, oom bool) string {
		be := labBackend()
		s := &be.state["atlas-dev"].sessions[3] // auth-refactor, needs input
		s.Reason, s.OOM = question, oom
		hs := newHarness(t, be, w, 24)
		hs.m.motion = false
		hs.key("enter")
		if sel := hs.m.selected(); sel == nil || sel.ID != "s4" {
			t.Fatalf("selected %v", sel)
		}
		return strings.Split(plainScreen(hs.m), "\n")[hs.m.geo.detailY]
	}
	if with, without := line(80, true), line(80, false); with != without || strings.Contains(with, capNote) {
		t.Errorf("at 80 columns the note changes the line:\n%q\n%q", with, without)
	}
	if got := line(120, true); !strings.Contains(got, "● "+question+"   ") || !strings.Contains(got, capNote) || cells(got) > 120 {
		t.Errorf("at 120 columns the line is %q", got)
	}
}

// Tips say nothing about the agent's work, but a tool's result under "⎿"
// says what it did.
func TestActivityLineSkipsTips(t *testing.T) {
	screen := "\x1b[2J\x1b[H" +
		"● Loading all 40 files at once\r\n" +
		"  ⎿  Read 31 of 40 files (2.1 GB)\r\n" +
		"  ⎿  Tip: Use /btw to ask a quick side question without interrupting\r\n" +
		"Tip: Press Tab to queue a message\r\n"
	if got := activityLine(renderScreen([]byte(screen), 80, 10)); got != "⎿ Read 31 of 40 files (2.1 GB)" {
		t.Fatalf("activity %q", got)
	}
}

// The demo has an agent that the memory cap stopped, for the screenshots
// and for people who try the launcher.
func TestDemoHasAnAgentTheMemoryCapStopped(t *testing.T) {
	be := newDemoBackend(false)
	for _, s := range be.sessions["gcp-dev"] {
		if !s.OOM {
			continue
		}
		if _, label := endLook(s); !ended(s) || !s.Alive || label.s != "out of memory" || endWhy(s) != "stopped at its memory cap of 6 GiB" {
			t.Fatalf("the demo's stopped agent looks like %q, %q", label.s, endWhy(s))
		}
		return
	}
	t.Fatal("no session in the demo was stopped by the memory cap")
}
