package host_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestAttachReplaysThenStreams(t *testing.T) {
	hosttest.New(t)
	// The agent puts its terminal in raw mode, so its bytes reach the ring
	// unchanged, and waits for a line of input before it writes more.
	s := start(t, "stream", "/bin/sh", "-c", "stty raw -echo; printf 'FIRST\\n'; head -c 1 >/dev/null; printf 'SECOND\\033[?2004h\\n'; exec sleep 60")
	waitOutput(t, s.ID, "FIRST")

	b := newRawBridge(t)
	a := b.attach(s.ID, -1, 100, 30)
	if a.Mode != "full" || a.From != 0 || a.Live != 6 || a.Exited {
		t.Fatalf("attached = %+v, want the whole ring from 0", a)
	}
	if got := b.output(0, "FIRST"); string(got) != "FIRST\n" {
		t.Fatalf("history = %q", got)
	}
	b.send(proto.TInput, []byte("x"))
	if got := b.output(6, "SECOND"); !bytes.Equal(got, []byte("SECOND\x1b[?2004h\n")) {
		t.Fatalf("live output = %q", got)
	}
	hosttest.WaitFor(t, 5*time.Second, "the attach to size the session", func() bool {
		m := live(t, s.ID)
		return m.Term.Cols == 100 && m.Term.Rows == 30
	})

	// A second client that already has the first 6 bytes gets only the rest.
	b2 := newRawBridge(t)
	a2 := b2.attach(s.ID, 6, 80, 24)
	if a2.Mode != "replay" || a2.From != 6 {
		t.Fatalf("attached = %+v, want a replay from 6", a2)
	}
	if got := b2.output(6, "SECOND"); !strings.HasPrefix(string(got), "SECOND") {
		t.Fatalf("replay = %q", got)
	}
	// An offset the ring no longer has means a full redraw.
	if a3 := newRawBridge(t).attach(s.ID, 1<<40, 80, 24); a3.Mode != "full" {
		t.Fatalf("attach from an unknown offset = %+v", a3)
	}
}

func TestAttachInputAndResize(t *testing.T) {
	hosttest.New(t)
	s := start(t, "io", "/bin/sh")
	b := newRawBridge(t)
	b.attach(s.ID, -1, 80, 24)
	b.send(proto.TInput, []byte("stty size; echo IN-$((1+1))\r"))
	b.output(0, "IN-2")
	b.sendJSON(proto.TResize, proto.Resize{Cols: 132, Rows: 43})
	hosttest.WaitFor(t, 5*time.Second, "the new size", func() bool {
		m := live(t, s.ID)
		return m.Term.Cols == 132 && m.Term.Rows == 43
	})
	b.send(proto.TInput, []byte("stty size\r"))
	waitOutput(t, s.ID, "43 132")
	// A detached client's keys and sizes go nowhere.
	b.send(proto.TDetach, nil)
	b.send(proto.TInput, []byte("echo GONE\r"))
	b.request(1, "ls", nil) // the bridge has read the frames above
	time.Sleep(100 * time.Millisecond)
	if strings.Contains(peek(t, s.ID), "GONE") {
		t.Fatal("input after detach reached the session")
	}
}

func TestAttachToAnEndedSession(t *testing.T) {
	hosttest.New(t)
	s := start(t, "ends", "/bin/sh", "-c", "head -c 1 >/dev/null; echo BYE; exit 7")
	b := newRawBridge(t)
	b.attach(s.ID, -1, 80, 24)
	b.send(proto.TInput, []byte("x\r"))
	var e proto.Ended
	if err := json.Unmarshal(b.next(proto.TEnded).Payload, &e); err != nil || e.ExitCode != 7 {
		t.Fatalf("ended = %+v, %v", e, err)
	}
	if !strings.Contains(peek(t, s.ID), "BYE") {
		t.Error("the last output is lost")
	}
	// A client that comes later learns it at once, after the output.
	b2 := newRawBridge(t)
	if a := b2.attach(s.ID, -1, 80, 24); !a.Exited {
		t.Fatalf("attached = %+v, want exited", a)
	}
	b2.output(0, "BYE")
	b2.next(proto.TEnded)
	if _, err := host.HolderRequest(s.ID, "send", map[string]any{"text": "x"}, time.Second); err == nil {
		t.Error("send to an ended session worked")
	}
}

func TestAttachedClientSeesTheSessionGo(t *testing.T) {
	hosttest.New(t)
	s := start(t, "gone", "sleep", "60")
	b := newRawBridge(t)
	b.attach(s.ID, -1, 80, 24)
	if _, err := host.Delete(s.ID); err != nil {
		t.Fatal(err)
	}
	var pe proto.Error
	if err := json.Unmarshal(b.next(proto.TError).Payload, &pe); err != nil || pe.Code != "session_gone" {
		t.Fatalf("error = %+v, %v", pe, err)
	}
}

func TestAttachFailures(t *testing.T) {
	hosttest.New(t)
	b := newRawBridge(t)
	for _, id := range []string{"../x", "nosuchid"} {
		b.sendJSON(proto.TAttach, proto.Attach{Session: id, LastOffset: -1})
		var pe proto.Error
		if err := json.Unmarshal(b.next(proto.TError).Payload, &pe); err != nil || pe.Code != "attach_failed" || pe.Hint == "" {
			t.Fatalf("attach %q: %+v, %v", id, pe, err)
		}
	}
	// Keys with no session attached go nowhere, and the bridge carries on.
	b.send(proto.TInput, []byte("x"))
	b.send(proto.TPing, []byte("p"))
	if f := b.next(proto.TPong); string(f.Payload) != "p" {
		t.Fatalf("pong = %q", f.Payload)
	}
}

func TestBridgeRefusesAnOldClient(t *testing.T) {
	hosttest.New(t)
	in := bytes.NewBuffer(nil)
	w := proto.NewWriter(in)
	_ = w.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Protocol: "0.1"})
	var out bytes.Buffer
	if err := host.RunBridge(in, &out); err == nil {
		t.Fatal("the bridge took an old protocol")
	}
	if !bytes.Contains(out.Bytes(), []byte(`"protocol"`)) || !bytes.Contains(out.Bytes(), []byte("Update the client")) {
		t.Fatalf("the bridge did not say why: %q", out.Bytes())
	}
	for _, first := range []proto.Type{proto.TPing, proto.THello} {
		in.Reset()
		if first == proto.THello {
			_ = w.Write(proto.THello, []byte("not json"))
		} else {
			_ = w.Write(first, nil)
		}
		if err := host.RunBridge(in, &out); err == nil {
			t.Errorf("the bridge took a first frame of type %d", first)
		}
	}
}

func TestSnapshotFromTheShadowTerminal(t *testing.T) {
	vt := host.ShadowVT()
	if vt == "" {
		t.Skip("the shadow terminal is not built: install Zig 0.16 or set VT_BINARY (see vt/README.md)")
	}
	h := hosttest.New(t)
	defer host.SetExecutable(h.Install("test", vt))()
	s := start(t, "vt", "/bin/sh", "-c", "printf '\\033[2J\\033[HTOP\\033[5;3HMIDDLE'; exec sleep 60")
	waitOutput(t, s.ID, "MIDDLE")
	b := newRawBridge(t)
	a := b.attach(s.ID, -1, 80, 24)
	if a.Mode != "snapshot" {
		t.Fatalf("attached = %+v, want a snapshot", a)
	}
	snap := b.next(proto.TSnapshot).Payload
	if !bytes.Contains(snap, []byte("TOP")) || !bytes.Contains(snap, []byte("MIDDLE")) {
		t.Fatalf("snapshot = %q", snap)
	}
	if strings.Contains(h.HolderLog(s.ID), "no shadow terminal") {
		t.Error("the holder did not find the shadow terminal next to it")
	}
}

// A client that left the alternate screen on detach asks for a snapshot. It
// must put the terminal back on the alternate screen, with every cell (#36).
func TestSnapshotOfTheAlternateScreen(t *testing.T) {
	vt := host.ShadowVT()
	if vt == "" {
		t.Skip("the shadow terminal is not built: install Zig 0.16 or set VT_BINARY (see vt/README.md)")
	}
	h := hosttest.New(t)
	defer host.SetExecutable(h.Install("test", vt))()
	s := start(t, "alt", "/bin/sh", "-c", "printf 'MAIN\\033[?1049h\\033[HTOP\\033[5;3HMIDDLE'; exec sleep 60")
	waitOutput(t, s.ID, "MIDDLE")
	b := newRawBridge(t)
	if a := b.attach(s.ID, -1, 80, 24); a.Mode != "snapshot" {
		t.Fatalf("attached = %+v, want a snapshot", a)
	}
	lines, alt := hosttest.Screen(b.next(proto.TSnapshot).Payload, 80, 24)
	if !alt || lines[0] != "TOP" || lines[4] != "  MIDDLE" {
		t.Errorf("after the snapshot, the alternate screen = %v, shows %q; want TOP and MIDDLE", alt, lines[:5])
	}
}

// A client gets all of the agent's output before it hears that the agent
// ended: the last words of a program are often why it ended. The output is
// written just before the exit, so the end races with the reading of it;
// many sessions at once make the end likelier to win.
func TestLastOutputComesBeforeTheEnd(t *testing.T) {
	hosttest.New(t)
	const n = 16
	ids := make([]string, n)
	var wg sync.WaitGroup
	for i := range ids {
		wg.Go(func() {
			s, err := host.New(host.NewArgs{Name: fmt.Sprint("last", i), Cwd: "~", Argv: []string{"/bin/sh", "-c",
				"stty raw -echo; printf 'START'; head -c 1 >/dev/null; printf 'LAST-OUTPUT'; exit 3"}})
			if err != nil {
				t.Error(err)
				return
			}
			ids[i] = s.ID
		})
	}
	wg.Wait()
	if t.Failed() {
		t.FailNow()
	}
	bridges := make([]*rawBridge, n)
	for i, id := range ids {
		waitOutput(t, id, "START")
		bridges[i] = newRawBridge(t)
		bridges[i].attach(id, -1, 80, 24)
	}
	for _, b := range bridges {
		b.send(proto.TInput, []byte("x"))
	}
	for i, b := range bridges {
		var got []byte
		for {
			f := b.next(proto.TOutput, proto.TEnded)
			if f.Type == proto.TEnded {
				break
			}
			_, data, _ := proto.DecodeOutput(f.Payload)
			got = append(got, data...)
		}
		if !bytes.Contains(got, []byte("LAST-OUTPUT")) {
			t.Errorf("session %d: the end came after only %q", i, got)
		}
	}
}
