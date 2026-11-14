package client

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
)

// Both queues wait for the output boundary. The paste guard observes every
// output byte, user notices all survive, and background notices stay bounded.
func TestNotificationQueuesKeepPasteGuardAndUserNotices(t *testing.T) {
	setHome(t)
	a, terminal := testAttacher(t)
	a.o.Host.Name = "box"
	prefix := []byte("\x1b]52;c;" + b64(victim))
	suffix := []byte("\x1b\\OK")
	a.show(t.Context(), prefix)
	var notices [][]byte
	for i := range 20 {
		message := fmt.Sprintf("uploaded file%d.png", i)
		a.notify(t.Context(), message)
		notices = append(notices, []byte("\x1b]777;notify;"+brand.DisplayName+" · box;"+message+"\x1b\\"))
	}
	var background [][]byte
	for i := range maxPendingNotify + 3 {
		seq := []byte(fmt.Sprintf("\x1b]777;notify;other;background%d\x1b\\", i))
		a.emitNotify(t.Context(), seq)
		background = append(background, seq)
	}
	if got := terminal(); !bytes.Equal(got, prefix) {
		t.Fatalf("a queued notice changed an unfinished clipboard sequence: %q", got)
	}
	a.show(t.Context(), suffix)
	if !remembersVictim(t.Context(), a.guard) {
		t.Fatal("the notification wrapper bypassed the paste observer")
	}
	got := terminal()
	for _, notice := range notices {
		if bytes.Count(got, notice) != 1 {
			t.Fatalf("a user notice was dropped or repeated: %q", notice)
		}
		got = bytes.ReplaceAll(got, notice, nil)
	}
	last := -1
	for i, notice := range background {
		at := bytes.Index(got, notice)
		if i < 3 {
			if at >= 0 {
				t.Fatal("an evicted background notice reappeared")
			}
			continue
		}
		if at <= last {
			t.Fatal("the background FIFO lost its order")
		}
		last = at
	}
	for _, notice := range background {
		got = bytes.ReplaceAll(got, notice, nil)
	}
	if strings.Contains(string(got), "777;notify") || !bytes.Equal(got, append(prefix, suffix...)) {
		t.Fatalf("the queues changed session bytes: %q", got)
	}
}
