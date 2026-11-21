package tui

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/client"
)

// order checks that the calls come in this order, each one there.
func order(t *testing.T, be *fakeBackend, calls ...string) {
	t.Helper()
	got := be.callList()
	at := -1
	for _, c := range calls {
		i := slices.Index(got, c)
		if i < 0 || i < at {
			t.Fatalf("want %q in this order, have %q", calls, got)
		}
		at = i
	}
}

// noCall checks that no call starts with prefix.
func noCall(t *testing.T, be *fakeBackend, prefix string) {
	t.Helper()
	for _, c := range be.callList() {
		if strings.HasPrefix(c, prefix) {
			t.Fatalf("a call %q in %q", c, be.callList())
		}
	}
}

// The wizard pins as host add does: it stages the pin before its first
// connection, and saves the host with it.
func TestWizardPinsFirst(t *testing.T) {
	be := newFakeBackend()
	be.probe = client.Platform{OS: "linux", Arch: "arm64"}
	hs := newHarness(t, be, 80, 24)
	hs.key("a")
	hs.typeText("pi@raspberry")
	hs.key("enter", "enter", "1", "enter")
	w := hs.m.modal.(*wizard)
	if !w.saved {
		t.Fatalf("not saved: step %v err %v", w.step, w.err)
	}
	order(t, be, "stage raspberry", "pin raspberry ", "probe raspberry", "install raspberry", "save raspberry pin="+fakeHostKey)
}

// A key that the user's settings do not name: the wizard shows its
// fingerprint and the command that shows it on the machine, and asks.
// Nothing connects before the yes, and a no saves nothing.
func TestWizardAsksAboutAnUnknownKey(t *testing.T) {
	be := newFakeBackend()
	be.probe = client.Platform{OS: "linux", Arch: "arm64"}
	be.pinUnknown = true
	hs := newHarness(t, be, 80, 24)
	hs.key("a")
	hs.typeText("pi@raspberry")
	hs.key("enter")
	w := hs.m.modal.(*wizard)
	if w.ask == nil || w.step != wzTest || w.err != nil {
		t.Fatalf("no question: step %v ask %v err %v", w.step, w.ask, w.err)
	}
	out := plainScreen(hs.m)
	for _, want := range []string{fakeHostKey, "ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub", "enter pin this key"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q:\n%s", want, out)
		}
	}
	noCall(t, be, "probe ")
	// A no: back to the first step, and the stage goes.
	hs.key("esc")
	if w.step != wzWhere || w.ask != nil {
		t.Fatalf("esc: step %v ask %v", w.step, w.ask)
	}
	hasCall(t, be, "abort raspberry")
	noCall(t, be, "probe ")
	noCall(t, be, "save ")
	// The yes pins that fingerprint, and the wizard goes on.
	hs.key("enter", "enter")
	if w.ask != nil || w.step != wzTest || w.err != nil {
		t.Fatalf("after the yes: step %v ask %v err %v", w.step, w.ask, w.err)
	}
	hs.key("enter", "1", "enter")
	if !w.saved {
		t.Fatalf("not saved: step %v err %v", w.step, w.err)
	}
	order(t, be, "pin raspberry "+fakeHostKey, "probe raspberry", "save raspberry pin="+fakeHostKey)
}

// Quitting the launcher with a pin staged and the host not saved removes
// the stage, and Esc or Close before StagePin runs opens no stage after it.
func TestWizardQuitRemovesItsStage(t *testing.T) {
	be := newFakeBackend()
	be.pinUnknown = true
	hs := newHarness(t, be, 80, 24)
	hs.key("a")
	hs.typeText("box")
	hs.key("enter")
	hs.m.Close()
	hasCall(t, be, "abort box")
	noCall(t, be, "save ")

	for _, stop := range []string{"esc", "close"} {
		t.Run(stop, func(t *testing.T) {
			be := newFakeBackend()
			gate := make(chan struct{})
			prev := beforeStagePin
			beforeStagePin = func() {
				be.blocked.Add(1)
				<-gate
				be.blocked.Add(-1)
			}
			t.Cleanup(func() { beforeStagePin = prev })
			hs := newHarness(t, be, 80, 24)
			hs.key("a")
			hs.typeText("box")
			hs.key("enter")
			if stop == "esc" {
				hs.key("esc")
			} else {
				hs.m.Close()
			}
			close(gate)
			hs.do(nil)
			noCall(t, be, "stage ")
			noCall(t, be, "pin ")
		})
	}
}

// A pin that fails stops the wizard before any connection, with the pin's
// own error.
func TestWizardStopsOnAFailedPin(t *testing.T) {
	be := newFakeBackend()
	be.pinErr = &client.UserError{Msg: "box shows the host key SHA256:x, and your known_hosts holds another key for it",
		Hint: "If you reinstalled box, pass its key with --host-key. Otherwise, do not connect.", Code: "host_key_changed", Kind: client.KindRefused}
	hs := newHarness(t, be, 80, 24)
	hs.key("a")
	hs.typeText("box")
	hs.key("enter")
	w := hs.m.modal.(*wizard)
	if msg, hint := errText(w.err); !strings.Contains(msg, "holds another key") || !strings.Contains(hint, "--host-key") || w.ask != nil {
		t.Fatalf("error %q %q, ask %v", msg, hint, w.ask)
	}
	noCall(t, be, "probe ")
	hs.key("esc", "esc")
	hasCall(t, be, "abort box")
	noCall(t, be, "save ")
}

// heldGate returns a channel and an idempotent close function, registered in
// t.Cleanup after newHarness so a failed check unblocks Close.
func heldGate(t *testing.T, ch chan struct{}) func() {
	var once sync.Once
	open := func() { once.Do(func() { close(ch) }) }
	t.Cleanup(open)
	return open
}

// Esc while the save of the host with its pin runs: the wizard waits for
// the save, which saves the host with its pin, and the stage goes only
// after it.
func TestWizardEscWaitsForTheSave(t *testing.T) {
	be := newFakeBackend()
	be.probe = client.Platform{OS: "linux", Arch: "arm64"}
	gate := make(chan struct{})
	be.saveGate = gate
	hs := newHarness(t, be, 80, 24)
	open := heldGate(t, gate)
	hs.key("a")
	hs.typeText("pi@raspberry")
	hs.key("enter", "enter", "1", "enter")
	w := hs.m.modal.(*wizard)
	if w.saving == nil || w.saved {
		t.Fatalf("the save is not held: saving %v, saved %v", w.saving != nil, w.saved)
	}
	hs.key("esc")
	if hs.m.modal != w || w.step != wzReady {
		t.Fatalf("Esc went on while the save ran: modal %T, step %v", hs.m.modal, w.step)
	}
	noCall(t, be, "abort ")
	open()
	hs.do(nil)
	if !w.saved {
		t.Fatalf("not saved: %v", w.err)
	}
	hasCall(t, be, "save raspberry pin="+fakeHostKey)
	noCall(t, be, "abort ")
}

// The launcher's end while the save runs: Close waits for the save, then
// removes what is left of the stage, which is nothing.
func TestWizardCloseWaitsForTheSave(t *testing.T) {
	be := newFakeBackend()
	be.probe = client.Platform{OS: "linux", Arch: "arm64"}
	gate := make(chan struct{})
	be.committedSaveGate = gate
	hs := newHarness(t, be, 80, 24)
	open := heldGate(t, gate)
	hs.key("a")
	hs.typeText("pi@raspberry")
	hs.key("enter", "enter", "1", "enter")
	// Hold the result of the actual successful CAS. A pre-commit wait is
	// cancellable, so it cannot establish this post-commit join contract.
	be.mu.Lock()
	committed := len(be.saved) == 1 && be.tiers["raspberry"].Key == fakeHostKey
	be.mu.Unlock()
	if !committed {
		t.Fatal("the held save did not commit its exact pin before Close")
	}
	closed := make(chan struct{})
	go func() {
		hs.m.Close()
		close(closed)
	}()
	select {
	case <-closed:
		t.Fatal("Close did not wait for the save")
	case <-time.After(200 * time.Millisecond):
	}
	open()
	<-closed
	calls := be.callList()
	save := slices.Index(calls, "save raspberry pin="+fakeHostKey)
	if save < 0 || slices.ContainsFunc(calls[:save], func(c string) bool { return strings.HasPrefix(c, "abort ") }) {
		t.Errorf("calls %q: want the save with its pin, and no abort before it", calls)
	}
}

// The launcher's remove forgets the host as host rm does, with its pin.
func TestRemoveForgetsThePin(t *testing.T) {
	hs := home(t, 80, 24)
	hs.key("ctrl+o")
	hs.typeText("host remove")
	hs.key("enter", "enter")
	hasCall(t, hs.be, "forget atlas-dev")
}

// While StagePin waits for the pin's lock, Esc and Close return right away
// without freezing the launcher, cancel StagePin, and leave no stage open.
func TestWizardEscAndCloseWhileStageWaits(t *testing.T) {
	for _, stop := range []string{"esc", "close"} {
		t.Run("fake/"+stop, func(t *testing.T) {
			be := newFakeBackend()
			gate := make(chan struct{})
			be.stageGate = gate
			hs := newHarness(t, be, 80, 24)
			open := heldGate(t, gate)
			defer open()
			hs.key("a")
			hs.typeText("box")
			hs.key("enter")
			if be.blocked.Load() == 0 {
				t.Fatal("StagePin did not block on stageGate")
			}
			done := make(chan struct{})
			go func() {
				if stop == "esc" {
					hs.key("esc")
				} else {
					hs.m.Close()
				}
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(500 * time.Millisecond):
				t.Fatalf("%s blocked while StagePin waited", stop)
			}
			hs.do(nil)
			noCall(t, be, "stage ")
			noCall(t, be, "pin ")
		})

		t.Run("real-lock/"+stop, func(t *testing.T) {
			if runtime.GOOS == "windows" {
				t.Skip("the fake ssh is a POSIX shell script")
			}
			cfg := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", cfg)
			bin := t.TempDir()
			started := filepath.Join(bin, "started")
			script := "#!/bin/sh\ncase \" $* \" in *\" -G \"*) echo 'stricthostkeychecking ask'; exit 0;; esac\ntouch \"" + started + "\"\nexec sleep 10\n"
			if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			// Hold the real exclusive pin lock of box via PinHostKey.
			hctx, hcancel := context.WithCancel(context.Background())
			holderDone := make(chan struct{})
			var holderErr error
			go func() {
				_, holderErr = client.PinHostKey(hctx, client.Host{Name: "box", SSH: "box"}, "", client.FirstUseAccept)
				close(holderDone)
			}()
			t.Cleanup(func() { hcancel(); <-holderDone })
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				if _, err := os.Stat(started); err == nil {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if _, err := os.Stat(started); err != nil {
				select {
				case <-holderDone:
					t.Fatalf("lock holder stopped before SSH: %v", holderErr)
				default:
					t.Fatal("lock holder did not reach SSH before the existing deadline")
				}
			}

			m := New(context.Background(), realBackend{}, Options{})
			t.Cleanup(m.closeAfterProgram)
			w := newWizard()
			m.modal = w
			w.step = wzTest
			w.host = client.Host{Name: "box", SSH: "box"}
			cmd := w.probe(m)
			ch := make(chan any, 1)
			go func() { ch <- cmd() }()
			// Give the probe goroutine a moment to block inside lockPin.
			time.Sleep(60 * time.Millisecond)
			done := make(chan struct{})
			go func() {
				if stop == "esc" {
					w.back(m)
				} else {
					m.Close()
				}
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(500 * time.Millisecond):
				t.Fatalf("%s blocked while the pin lock was held", stop)
			}
			select {
			case <-ch:
			case <-time.After(500 * time.Millisecond):
				t.Fatalf("StagePin did not stop on cancellation (%s)", stop)
			}
			hcancel()
			<-holderDone
			entries, _ := os.ReadDir(filepath.Join(client.ConfigDir(), "ssh"))
			for _, e := range entries {
				if strings.Contains(e.Name(), "known_hosts-") && !strings.HasSuffix(e.Name(), ".lock") {
					t.Fatalf("left a stage or pin file behind: %s", e.Name())
				}
			}
		})
	}

	// If stop races with StagePin finishing just before ctx cancels,
	// wizOp.stage aborts the stage right away.
	t.Run("stop-races-with-stage-return", func(t *testing.T) {
		be := newFakeBackend()
		gate := make(chan struct{})
		be.stageGate = gate
		w := newWizard()
		w.tok = &wizOp{}
		tok := w.tok
		res := make(chan bool, 1)
		go func() {
			_, ok, _ := tok.stage(context.Background(), be, "box")
			res <- ok
		}()
		for be.blocked.Load() == 0 {
			time.Sleep(5 * time.Millisecond)
		}
		w.stop()
		close(gate)
		if ok := <-res; ok {
			t.Fatal("stage reported active after stop")
		}
		order(t, be, "stage box", "abort box")
	})
}
