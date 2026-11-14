package client

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
)

func TestClipboardLookupCancelsWhileMemoryLockIsHeld(t *testing.T) {
	m, _ := testMemory(t)
	path := filepath.Join(t.TempDir(), "drop.png")
	must0(t, os.WriteFile(path, []byte("a local file"), 0o600))
	must0(t, os.MkdirAll(filepath.Dir(m.path), 0o700))
	unlock, err := lockPathContext(t.Context(), m.path+".lock", clipWait, true)
	if err != nil {
		t.Fatal(err)
	}
	var released sync.Once
	release := func() { released.Do(unlock) }
	defer release()
	parent, cancel := context.WithCancel(t.Context())
	defer cancel()
	ctx := &observedTrustWaitContext{Context: parent, observed: make(chan struct{})}
	done := make(chan string, 1)
	go func() {
		drop, why := newPasteGuard(m).check(ctx, []byte(path), []string{path})
		if drop != nil {
			drop.close()
			why = "allowed an unchecked drop: " + why
		}
		done <- why
	}()
	joined := false
	defer func() {
		cancel()
		release()
		if !joined {
			<-done
		}
	}()
	select {
	case <-ctx.observed:
	case <-time.After(clipWait):
		t.Fatal("the clipboard lookup never waited with its operation context")
	}
	cancel()
	select {
	case why := <-done:
		joined = true
		if !strings.Contains(why, "could not be checked") || !strings.Contains(why, context.Canceled.Error()) || strings.Contains(why, "allowed an unchecked drop") {
			t.Fatalf("cancelled lookup did not fail closed while the lock stayed held: %q", why)
		}
	case <-time.After(clipWait):
		t.Fatal("cancellation could not leave the clipboard lock wait")
	}
}

func TestCancelledClipboardOutputAndDetachKeepDurableHistory(t *testing.T) {
	for _, format := range []string{"osc52", "iterm2", "kitty"} {
		t.Run(format, func(t *testing.T) {
			a, terminal := testAttacher(t)
			m := a.guard.mem
			path := filepath.Join(t.TempDir(), "drop.png")
			var data string
			switch format {
			case "osc52":
				data = osc52(path)
			case "iterm2":
				data = "\x1b]1337;CopyToClipboard\a" + path
			case "kitty":
				data = "\x1b]5522;type=write\x1b\\\x1b]5522;type=wdata:mime=dGV4dC9wbGFpbg==;" + b64(path) + "\x1b\\"
			}
			// Pausing the existing saver proves that a completed write is
			// shared through the journal before any queued merge can hide it.
			oldPause := clipPauseSave
			clipPauseSave = true
			t.Cleanup(func() { clipPauseSave = oldPause })
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if _, err := a.frame(ctx, proto.Frame{Type: proto.TOutput, Payload: proto.EncodeOutput(0, []byte(data))}); err != nil {
				t.Fatal(err)
			}
			if got := terminal(); !bytes.Equal(got, []byte(data)) {
				t.Fatalf("cancelled output changed the terminal bytes: %q", got)
			}
			wantSum := sumOf(t, path)
			if format == "osc52" {
				if why, match := otherProcess(m).check(t.Context(), [][32]byte{wantSum}, nil); match != 0 || !strings.Contains(why, "put on your clipboard") {
					t.Fatalf("the cancelled write was not journalled before terminal output: %q, %d", why, match)
				}
				if _, err := os.Stat(m.path); !os.IsNotExist(err) {
					t.Fatalf("the paused saver made a shared file: %v", err)
				}
			}
			// No saver was started while paused. Resume before detach, so
			// its cancelled-context cleanup must actually drain the queue.
			clipPauseSave = oldPause
			notice := []byte("\x1b]777;notify;copy;done\x1b\\")
			a.held = append(a.held, heldNotice{seq: notice, keep: true})
			reset := a.tracker.M.ResetSequence()
			a.write(ctx, reset)
			a.endGuard(ctx)
			title := []byte("\x1b]9;4;0\x1b\\\x1b[23;0t")
			a.write(ctx, title)
			want := append([]byte(data), reset...)
			if format == "iterm2" {
				want = append(want, "\x1b]1337;EndCopy\a"...)
			}
			want = append(append(want, notice...), title...)
			if got := terminal(); !bytes.Equal(got, want) {
				t.Fatalf("detach changed or reordered terminal bytes: got %q, want %q", got, want)
			}
			have, err := readClipFile(m.path)
			if err != nil || !have.has(wantSum) {
				t.Fatalf("cancelled detach did not fold the observed write into the shared file: %v", err)
			}
			journal, err := os.ReadFile(journalPath(m.path))
			if err != nil || len(journal) != 0 {
				t.Fatalf("cancelled detach left the journal undrained: %d bytes, %v", len(journal), err)
			}
			if why, match := otherProcess(m).check(t.Context(), [][32]byte{wantSum}, nil); match != 0 || !strings.Contains(why, "put on your clipboard") {
				t.Fatalf("another client forgot the cancelled attach's clipboard write: %q, %d", why, match)
			}
		})
	}
}
