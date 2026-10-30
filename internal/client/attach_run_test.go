//go:build !windows

// These tests run the real host side, which runs on Linux and macOS only.

package client_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/client/clienttest"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/paths"
)

// The tests in this binary may start it again as a session's holder.
func TestMain(m *testing.M) { hosttest.Main(m) }

// rawReady is what the client writes once the terminal is in raw mode: it
// saves the window title. Keys typed before it would be cooked.
const rawReady = "\x1b[22;0t"

// detach is Ctrl+\ as a legacy terminal sends it.
const detach = "\x1c"

type attachRun struct {
	t     *testing.T
	h     *hosttest.Host
	ch    client.Host
	tr    *hosttest.Transport
	term  *hosttest.Terminal
	state client.AttachState
}

func newAttachRun(t *testing.T) *attachRun {
	t.Helper()
	h := hosttest.New(t)
	ch, tr := h.ClientHost("box")
	return &attachRun{t: t, h: h, ch: ch, tr: tr, term: hosttest.NewTerminal(t, 80, 24)}
}

// session starts a session that runs a shell script.
func (r *attachRun) session(script string) string {
	r.t.Helper()
	s, err := host.New(host.NewArgs{Name: "s", Cwd: "~", Argv: []string{"/bin/sh", "-c", script}, Cols: 80, Rows: 24})
	if err != nil {
		r.t.Fatal(err)
	}
	return s.ID
}

type attachEnd struct {
	res client.AttachResult
	err error
}

// attach runs client.Attach in the background.
func (r *attachRun) attach(ctx context.Context, id string, settings client.Settings) chan attachEnd {
	r.t.Helper()
	done := make(chan attachEnd, 1)
	go func() {
		res, err := client.Attach(ctx, client.AttachOptions{Host: r.ch, Session: id, Settings: settings, State: &r.state, In: r.term.TTY, Out: r.term.TTY})
		done <- attachEnd{res, err}
	}()
	return done
}

func (r *attachRun) end(done chan attachEnd) attachEnd {
	r.t.Helper()
	select {
	case e := <-done:
		return e
	case <-time.After(20 * time.Second):
		r.t.Fatalf("the attach did not end; the terminal shows %q", r.term.Output())
		return attachEnd{}
	}
}

// waitFile waits until a file on the host holds want, and returns it.
func waitFile(t *testing.T, path, want string) []byte {
	t.Helper()
	var b []byte
	hosttest.WaitFor(t, 10*time.Second, path+" to hold "+strconv.Quote(want), func() bool {
		b, _ = os.ReadFile(path)
		return bytes.Contains(b, []byte(want))
	})
	return b
}

func TestAttachIsByteExact(t *testing.T) { checkByteExact(t, newAttachRun(t), nil) }

// checkByteExact attaches to a session that prints every byte value and
// records every key, and checks that both reach the other end unchanged.
// While attached, it calls during, when it is not nil.
func checkByteExact(t *testing.T, r *attachRun, during func(id string)) {
	t.Helper()
	// Every byte value, and the sequences terminals care most about.
	data := append(clienttest.EveryByte(4096), []byte("\x1b]8;;https://example.com\x1b\\link\x1b]8;;\x1b\\ \x1b[38;2;255;100;0mtruecolor\x1b[0m "+
		"\x1b[4:3mcurly\x1b[0m \x1b]777;notify;Title;Body\x1b\\\x1b]52;c;aGVsbG8=\x1b\\\x1b[?2026h\x1b[?2026l END")...)
	file := filepath.Join(r.h.Home, "data.bin")
	if err := os.WriteFile(file, data, 0o600); err != nil {
		t.Fatal(err)
	}
	keys := filepath.Join(r.h.Home, "keys.bin")
	typed := "hello\x01\x7f\xc3\xa9 \x1bOA world\r\x00"
	id := r.session(fmt.Sprintf("stty raw -echo; head -c 1 >/dev/null; cat %s; head -c %d > %s; exec sleep 60", file, len(typed), keys))
	done := r.attach(context.Background(), id, client.DefaultSettings())
	r.term.WaitFor(rawReady)
	r.term.Type("x")
	if out := r.term.WaitFor(" END"); !bytes.Contains(out, data) {
		t.Fatal("the session's bytes changed on the way to the terminal")
	}
	r.term.Type(typed)
	if got := waitFile(t, keys, "\x00"); string(got) != typed {
		t.Fatalf("the agent got %q, want %q", got, typed)
	}
	if during != nil {
		during(id)
	}
	r.term.Type(detach)
	e := r.end(done)
	if e.err != nil || !e.res.Detached || e.res.Ended != nil {
		t.Fatalf("detach = %+v, %v", e.res, e.err)
	}
	out := r.term.WaitFor("\x1b[23;0t")
	if bytes.Contains(out, []byte("\x1bc")) {
		t.Error("RIS (ESC c) reached the terminal")
	}
	// On detach, the modes the session set are undone, and the title put back.
	if !bytes.HasSuffix(out, []byte("\x1b[23;0t")) {
		t.Errorf("the terminal was not put back: it ends %q", tail(out))
	}
	if !r.state.Valid || r.state.Offset != int64(len(data)) {
		t.Errorf("state = %+v, want the offset after the data", r.state)
	}
}

func tail(b []byte) []byte {
	if len(b) > 80 {
		return b[len(b)-80:]
	}
	return b
}

func TestDetachKeyTwiceGoesToTheAgent(t *testing.T) {
	r := newAttachRun(t)
	keys := filepath.Join(r.h.Home, "keys.bin")
	id := r.session("stty raw -echo; head -c 2 > " + keys + "; exec sleep 60")
	done := r.attach(context.Background(), id, client.DefaultSettings())
	r.term.WaitFor(rawReady)
	r.term.Type(detach + detach)
	time.Sleep(600 * time.Millisecond)
	r.term.Type("z")
	if got := waitFile(t, keys, "z"); string(got) != "\x1cz" {
		t.Fatalf("the agent got %q, want the detach key, then z", got)
	}
	r.term.Type(detach)
	if e := r.end(done); !e.res.Detached {
		t.Fatalf("attach = %+v, %v", e.res, e.err)
	}
}

// A lone Esc reaches the agent at once, as the same byte. The client holds
// only an unfinished key sequence, here for an hour, so an Esc that waited
// for the flush timer would never arrive.
func TestAttachSendsALoneEscAtOnce(t *testing.T) {
	defer client.SetEscFlush(time.Hour)()
	r := newAttachRun(t)
	keys := filepath.Join(r.h.Home, "keys.bin")
	id := r.session("stty raw -echo; printf 'READY'; exec cat > " + keys)
	done := r.attach(context.Background(), id, client.DefaultSettings())
	r.term.WaitFor("READY")
	r.term.Type("\x1b")
	if got := waitFile(t, keys, "\x1b"); string(got) != "\x1b" {
		t.Fatalf("the agent got %q, want a lone Esc", got)
	}
	// The rest of a paste's start marker, in the next read, still makes a
	// paste: the detach key in it goes to the agent.
	r.term.Type("[200~a\x1cb\x1b[201~")
	// The start of a key waits for the rest of it.
	r.term.Type("\x1b[1")
	time.Sleep(100 * time.Millisecond)
	if got, _ := os.ReadFile(keys); bytes.Contains(got, []byte("\x1b[1")) {
		t.Fatalf("the start of a key went out before its end: the agent got %q", got)
	}
	r.term.Type("1~")
	want := "\x1b[200~a\x1cb\x1b[201~\x1b[11~"
	if got := waitFile(t, keys, "\x1b[11~"); string(got) != want {
		t.Fatalf("the agent got %q, want %q", got, want)
	}
	r.term.Type(detach)
	if e := r.end(done); e.err != nil || !e.res.Detached {
		t.Fatalf("detach = %+v, %v", e.res, e.err)
	}
}

func TestAttachSeesTheSessionEnd(t *testing.T) {
	r := newAttachRun(t)
	id := r.session("stty raw -echo; head -c 1 >/dev/null; printf 'BYE'; exit 5")
	done := r.attach(context.Background(), id, client.DefaultSettings())
	r.term.WaitFor(rawReady)
	r.term.Type("x")
	e := r.end(done)
	if e.err != nil || e.res.Ended == nil || e.res.Ended.ExitCode != 5 || e.res.Detached {
		t.Fatalf("attach = %+v, %v", e.res, e.err)
	}
	if r.state.Valid {
		t.Error("the state of an ended session is kept")
	}
	r.term.WaitFor("BYE")
}

func TestAttachSeesTheHolderDie(t *testing.T) {
	r := newAttachRun(t)
	id := r.session("printf 'HERE'; exec sleep 60")
	done := r.attach(context.Background(), id, client.DefaultSettings())
	r.term.WaitFor("HERE")
	// The holder dies, as in a crash: the bridge says the session is gone.
	pid := hosttest.HolderPID(t, id)
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	if err := hosttest.Reap(pid, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if e := r.end(done); e.err != nil || e.res.Ended == nil || e.res.Ended.ExitCode != -1 {
		t.Fatalf("attach = %+v, %v", e.res, e.err)
	}
}

func TestAttachReplaysWhatTheTerminalMissed(t *testing.T) {
	r := newAttachRun(t)
	id := r.session("stty raw -echo; printf 'ONE\\033[?2004h'; head -c 1 >/dev/null; printf 'TWO'; exec sleep 60")
	done := r.attach(context.Background(), id, client.DefaultSettings())
	r.term.WaitFor("ONE")
	r.term.Type(detach)
	r.end(done)
	if !r.state.Valid || !r.state.Modes.Private[2004] {
		t.Fatalf("state = %+v, want the session's bracketed paste mode", r.state)
	}
	sendAndWait(t, id, "TWO")
	// The same terminal attaches again: it gets only what it missed, after
	// the session's modes are put back, with no redraw.
	r.term.Clear()
	done = r.attach(context.Background(), id, client.DefaultSettings())
	out := r.term.WaitFor("TWO")
	r.term.Type(detach)
	r.end(done)
	if bytes.Contains(out, []byte("ONE")) {
		t.Errorf("the replay repeated what the terminal had: %q", out)
	}
	if !bytes.Contains(out, []byte("\x1b[?2004h")) {
		t.Errorf("the session's modes were not put back: %q", out)
	}
}

// sendAndWait types a key into the session while no client is attached, and
// waits until the session's output holds want.
func sendAndWait(t *testing.T, id, want string) {
	t.Helper()
	if _, err := host.HolderRequest(id, "send", map[string]any{"text": "x"}, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	hosttest.WaitFor(t, 5*time.Second, want+" in the session", func() bool {
		raw, _ := host.HolderRequest(id, "peek", nil, time.Second)
		var p struct{ Data []byte }
		return json.Unmarshal(raw, &p) == nil && bytes.Contains(p.Data, []byte(want))
	})
}

// Leaving the alternate screen on detach throws its cells away, so the same
// terminal cannot continue from its offset: the bytes missed since would
// paint a few cells on a blank screen (#36). It gets the whole screen again.
func TestAttachRedrawsTheAlternateScreen(t *testing.T) {
	r := newAttachRun(t)
	// A full-screen program that, like Claude Code's, then redraws only what changed.
	id := r.session("stty raw -echo; printf '\\033[?1049h\\033[HTOP\\033[5;3HMIDDLE'; head -c 1 >/dev/null; printf '\\033[9;1HNEW'; exec sleep 60")
	done := r.attach(context.Background(), id, client.DefaultSettings())
	r.term.WaitFor("MIDDLE")
	r.term.Type(detach)
	r.end(done)
	if !r.state.Valid || !r.state.Modes.AltScreen {
		t.Fatalf("state = %+v, want the session on the alternate screen", r.state)
	}
	sendAndWait(t, id, "NEW")
	r.term.Clear()
	done = r.attach(context.Background(), id, client.DefaultSettings())
	out := r.term.WaitFor("NEW")
	r.term.Type(detach)
	r.end(done)
	lines, alt := hosttest.Screen(out, 80, 24)
	if !alt || lines[0] != "TOP" || lines[4] != "  MIDDLE" || lines[8] != "NEW" {
		t.Errorf("after the attach, the alternate screen = %v, shows %q; want TOP, MIDDLE and NEW", alt, lines[:9])
	}
}

// The snapshot draws the main screen under the alternate one, so leaving the
// alternate screen on detach shows what the agent drew there before it went
// full screen. Detach empties that screen, so no old frame is left over the
// shell prompt (#38).
func TestDetachEmptiesTheMainScreen(t *testing.T) {
	for _, mode := range []string{"keep", "clear"} {
		t.Run(mode, func(t *testing.T) {
			r := newAttachRun(t)
			id := r.session("stty raw -echo; printf 'OLD FRAME\\r\\n\\033[?1049h\\033[HFULL'; exec sleep 60")
			settings := client.DefaultSettings()
			settings.Attach.Scrollback = mode
			done := r.attach(context.Background(), id, settings)
			r.term.WaitFor("FULL")
			r.term.Type(detach)
			r.end(done)
			out := r.term.WaitFor("\x1b[23;0t")
			lines, alt := hosttest.Screen(out, 80, 24)
			if alt || strings.Join(lines, "") != "" {
				t.Errorf("after the detach, the alternate screen = %v, the screen shows %q; want an empty main screen", alt, lines)
			}
			if sb := hosttest.Scrollback(out, 80, 24); mode == "keep" && !slices.Contains(sb, "OLD FRAME") {
				t.Errorf("the scrollback = %q, want the old frame in it", sb)
			}
		})
	}
}

func TestAttachReconnectsWithoutAGap(t *testing.T) {
	r := newAttachRun(t)
	id := r.session("stty raw -echo; head -c 1 >/dev/null; i=0; while [ $i -lt 80 ]; do i=$((i+1)); printf 'N%d\\n' $i; sleep 0.02; done; exec sleep 60")
	done := r.attach(context.Background(), id, client.DefaultSettings())
	r.term.WaitFor(rawReady)
	r.term.Type("x")
	r.term.WaitFor("N10\n")
	if r.tr.Drop() != 1 {
		t.Fatal("no connection to drop")
	}
	out := r.term.WaitFor("N80\n")
	r.term.Type(detach)
	e := r.end(done)
	if e.err != nil || e.res.Reconnects != 1 {
		t.Fatalf("attach = %+v, %v", e.res, e.err)
	}
	seen := map[int]int{}
	for _, m := range regexp.MustCompile(`N(\d+)\n`).FindAllSubmatch(out, -1) {
		n, _ := strconv.Atoi(string(m[1]))
		seen[n]++
	}
	for i := 1; i <= 80; i++ {
		if seen[i] != 1 {
			t.Errorf("line N%d arrived %d times", i, seen[i])
		}
	}
	if !bytes.Contains(out, []byte("reconnecting · box/"+id)) {
		t.Error("the title did not say it was reconnecting")
	}
	// The holder knows the second attach is the same person after a dropped
	// link, so it keeps a memory warning that they never saw.
	if log := r.h.HolderLog(id); strings.Count(log, "client attached again after a dropped link") != 1 {
		t.Errorf("the holder did not see one reconnect:\n%s", log)
	}
}

func TestAttachWaitsForAHostThatIsDown(t *testing.T) {
	r := newAttachRun(t)
	id := r.session("printf 'UP'; exec sleep 60")
	done := r.attach(context.Background(), id, client.DefaultSettings())
	r.term.WaitFor("UP")
	r.tr.SetDown(errors.New("connection refused"))
	r.tr.Drop()
	// The banner says what is going on, on the bottom row.
	r.term.WaitFor("connection lost · reconnecting")
	r.term.Type("k") // a key tries again at once; it is not sent
	r.tr.SetDown(nil)
	r.term.Type("k")
	hosttest.WaitFor(t, 10*time.Second, "a new connection", func() bool { return r.tr.Open() == 1 })
	r.term.Type(detach)
	e := r.end(done)
	if e.err != nil || !e.res.Detached || e.res.Reconnects != 1 {
		t.Fatalf("attach = %+v, %v", e.res, e.err)
	}
}

func TestDetachWhileReconnecting(t *testing.T) {
	r := newAttachRun(t)
	id := r.session("printf 'UP'; exec sleep 60")
	done := r.attach(context.Background(), id, client.DefaultSettings())
	r.term.WaitFor("UP")
	r.tr.SetDown(errors.New("connection refused"))
	r.tr.Drop()
	r.term.WaitFor("reconnecting")
	r.term.Type(detach)
	if e := r.end(done); e.err != nil || !e.res.Detached {
		t.Fatalf("attach = %+v, %v", e.res, e.err)
	}
}

func TestReconnectToADeletedSession(t *testing.T) {
	r := newAttachRun(t)
	id := r.session("printf 'UP'; exec sleep 60")
	done := r.attach(context.Background(), id, client.DefaultSettings())
	r.term.WaitFor("UP")
	r.tr.SetDown(errors.New("connection refused"))
	r.tr.Drop()
	r.term.WaitFor("reconnecting")
	if _, err := host.Delete(id); err != nil {
		t.Fatal(err)
	}
	r.tr.SetDown(nil)
	r.term.Type("k")
	e := r.end(done)
	var ue *client.UserError
	if !errors.As(e.err, &ue) || !strings.Contains(ue.Msg, "is not running") {
		t.Fatalf("attach = %+v, %v", e.res, e.err)
	}
}

func TestAttachResizes(t *testing.T) {
	r := newAttachRun(t)
	r.term.Resize(100, 30, false)
	id := r.session("exec sleep 60")
	done := r.attach(context.Background(), id, client.DefaultSettings())
	r.term.WaitFor(rawReady)
	size := func(cols, rows int) func() bool {
		return func() bool {
			// The holder's own copy: the file is saved only once a second.
			raw, err := host.HolderRequest(id, "info", nil, time.Second)
			var m host.Manifest
			return err == nil && json.Unmarshal(raw, &m) == nil && m.Term.Cols == cols && m.Term.Rows == rows
		}
	}
	hosttest.WaitFor(t, 5*time.Second, "the terminal's size", size(100, 30))
	r.term.Resize(120, 40, true)
	hosttest.WaitFor(t, 5*time.Second, "the new size", size(120, 40))
	r.term.Type(detach)
	r.end(done)
}

func TestAttachEndsWithItsContext(t *testing.T) {
	r := newAttachRun(t)
	id := r.session("exec sleep 60")
	ctx, cancel := context.WithCancel(context.Background())
	done := r.attach(ctx, id, client.DefaultSettings())
	r.term.WaitFor(rawReady)
	cancel()
	if e := r.end(done); e.err != nil || !e.res.Detached {
		t.Fatalf("attach = %+v, %v", e.res, e.err)
	}
}

func TestAttachClearsScrollbackWhenAsked(t *testing.T) {
	r := newAttachRun(t)
	id := r.session("printf 'HI'; exec sleep 60")
	s := client.DefaultSettings()
	s.Attach.Scrollback = "clear"
	done := r.attach(context.Background(), id, s)
	out := r.term.WaitFor("HI")
	r.term.Type(detach)
	r.end(done)
	if !bytes.Contains(out, []byte("\x1b[H\x1b[2J\x1b[3J")) {
		t.Errorf("the screen and scrollback were not cleared: %q", out)
	}
}

func TestAttachFailures(t *testing.T) {
	r := newAttachRun(t)
	bad := client.DefaultSettings()
	bad.Attach.DetachKey = "ctrl+nothing"
	ctx := context.Background()
	var ue *client.UserError
	if _, err := client.Attach(ctx, client.AttachOptions{Host: r.ch, Session: "x", Settings: bad}); !errors.As(err, &ue) || !strings.Contains(ue.Msg, "detach key") {
		t.Errorf("a bad detach key: %v", err)
	}
	bad = client.DefaultSettings()
	bad.Attach.ImagePasteKey = "ctrl+nothing"
	if _, err := client.Attach(ctx, client.AttachOptions{Host: r.ch, Session: "x", Settings: bad}); !errors.As(err, &ue) || !strings.Contains(ue.Msg, "image paste key") {
		t.Errorf("a bad image paste key: %v", err)
	}
	e := r.end(r.attach(ctx, "nosuchid", client.DefaultSettings()))
	if !errors.As(e.err, &ue) || !strings.Contains(ue.Msg, "not running") {
		t.Errorf("attach to no session: %v", e.err)
	}
	r.tr.SetDown(errors.New("connection refused"))
	if e := r.end(r.attach(ctx, "nosuchid", client.DefaultSettings())); e.err == nil {
		t.Error("attach to a host that is down worked")
	}
	// Not a terminal: the session is left as it was.
	r.tr.SetDown(nil)
	id := r.session("exec sleep 60")
	f, err := os.CreateTemp(t.TempDir(), "not-a-tty")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := client.Attach(ctx, client.AttachOptions{Host: r.ch, Session: id, Settings: client.DefaultSettings(), In: f, Out: f}); err == nil || !strings.Contains(err.Error(), "raw mode") {
		t.Errorf("attach without a terminal = %v", err)
	}
}

func TestDroppedFilesAreUploaded(t *testing.T) {
	r := newAttachRun(t)
	got := filepath.Join(r.h.Home, "pasted")
	id := r.session("stty raw -echo; printf '\\033[?2004hREADY'; exec cat > " + got)
	done := r.attach(context.Background(), id, client.DefaultSettings())
	r.term.WaitFor("READY")
	local := filepath.Join(t.TempDir(), "my notes.txt")
	if err := os.WriteFile(local, []byte("dropped file content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A terminal pastes a dropped file's path, quoted.
	r.term.Type("\x1b[200~" + strings.ReplaceAll(local, " ", "\\ ") + "\x1b[201~")
	// Text that is not a file goes as it is.
	r.term.Type("\x1b[200~just text\x1b[201~")
	b := waitFile(t, got, "just text")
	m := regexp.MustCompile(`\x1b\[200~(\S+) \x1b\[201~`).FindSubmatch(b)
	if m == nil {
		t.Fatalf("the agent got %q, want the uploaded file's path in a bracketed paste", b)
	}
	if dir := filepath.Dir(string(m[1])); dir != paths.PasteDir(id) {
		t.Errorf("uploaded into %s, want %s", dir, paths.PasteDir(id))
	}
	if up, _ := os.ReadFile(string(m[1])); string(up) != "dropped file content\n" {
		t.Errorf("the upload holds %q", up)
	}
	if !bytes.Contains(b, []byte("\x1b[200~just text\x1b[201~")) {
		t.Errorf("a plain paste changed: %q", b)
	}
	r.term.Type(detach)
	r.end(done)
}

func TestImagePaste(t *testing.T) {
	r := newAttachRun(t)
	png := filepath.Join(t.TempDir(), "clip.png")
	if err := os.WriteFile(png, []byte("\x89PNG\r\n\x1a\nnot really"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(brand.Env("TEST_CLIPBOARD_IMAGE"), png)
	got := filepath.Join(r.h.Home, "pasted")
	id := r.session("stty raw -echo; printf 'READY'; exec cat > " + got)
	done := r.attach(context.Background(), id, client.DefaultSettings())
	r.term.WaitFor("READY")
	r.term.Type("\x16") // Ctrl+V
	b := waitFile(t, got, "clipboard.png ")
	path := strings.TrimSuffix(string(b), " ")
	if up, _ := os.ReadFile(path); !bytes.HasPrefix(up, []byte("\x89PNG")) {
		t.Errorf("the agent got %q; the file there holds %q", b, up)
	}
	// With no image on the clipboard, the key goes to the agent.
	t.Setenv(brand.Env("TEST_CLIPBOARD_IMAGE"), filepath.Join(t.TempDir(), "none.png"))
	r.term.Type("\x16")
	waitFile(t, got, "\x16")
	r.term.Type(detach)
	r.end(done)
}

// Each attach stops reading the terminal before it returns, so the next one
// (or the launcher) gets every key, and nothing reads a file it has closed.
// On Linux a reader left behind took the next attach's keys, and the race
// detector saw it close.
func TestAttachAgainAndAgain(t *testing.T) {
	r := newAttachRun(t)
	id := r.session("stty raw -echo; exec cat")
	for i := range 30 {
		ctx, cancel := context.WithCancel(context.Background())
		done := r.attach(ctx, id, client.DefaultSettings())
		r.term.WaitFor(rawReady)
		r.term.Clear()
		// The agent echoes what it gets: no earlier attach took the keys.
		mark := fmt.Sprintf("MARK%d.", i)
		r.term.Type(mark)
		r.term.WaitFor(mark)
		cancel()
		if e := r.end(done); e.err != nil || !e.res.Detached {
			t.Fatalf("attach %d = %+v, %v", i, e.res, e.err)
		}
	}
}
