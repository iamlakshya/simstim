package alerts

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestLogTurnsOver(t *testing.T) {
	dir := t.TempDir()
	l := &DeliveryLog{Path: filepath.Join(dir, "alerts.log"), Lock: filepath.Join(dir, "lock"), Max: 1000}
	for i := range 30 {
		if err := l.Append(api.AlertLogEntry{Time: time.Unix(int64(i), 0).UTC(), Event: Failed, Session: fmt.Sprintf("s%02d", i), OK: true}); err != nil {
			t.Fatal(err)
		}
	}
	st, err := os.Stat(l.Path)
	if err != nil || st.Size() > 1000 {
		t.Fatalf("the log is %d bytes, over its limit (%v)", st.Size(), err)
	}
	if _, err := os.Stat(l.Path + ".1"); err != nil {
		t.Fatalf("the log did not turn over: %v", err)
	}
	last, err := l.Tail(3)
	if err != nil || len(last) != 3 || last[2].Session != "s29" || last[0].Session != "s27" {
		t.Errorf("Tail(3) = %+v, %v", last, err)
	}
	// Tail reaches into the turned-over file when it must.
	cur, _ := readLog(l.Path)
	more, err := l.Tail(len(cur) + 2)
	if err != nil || len(more) != len(cur)+2 || more[len(more)-1].Session != "s29" {
		t.Errorf("Tail across files = %d entries, %v", len(more), err)
	}
	if none, _ := (&DeliveryLog{Path: filepath.Join(dir, "missing")}).Tail(5); len(none) != 0 {
		t.Error("a missing log has entries")
	}
}

func TestFileLimiter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rate")
	l := &FileLimiter{Path: path, Max: 3, Window: time.Minute}
	now := time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)
	for i := range 3 {
		if !l.Allow(now) {
			t.Fatalf("event %d refused", i+1)
		}
	}
	if l.Allow(now.Add(30 * time.Second)) {
		t.Error("a fourth event within a minute was allowed")
	}
	if !l.Allow(now.Add(61 * time.Second)) {
		t.Error("the window did not move on")
	}
	// Another holder (another limiter on the same file) sees the same count.
	if (&FileLimiter{Path: path, Max: 1, Window: time.Minute}).Allow(now.Add(62 * time.Second)) {
		t.Error("a second limiter did not see the shared count")
	}
}

func TestFileLimiterConcurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rate")
	now := time.Now()
	var allowed atomic.Int32
	var wg sync.WaitGroup
	for range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if (&FileLimiter{Path: path, Max: HostLimit, Window: time.Minute}).Allow(now) {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()
	if n := allowed.Load(); n != HostLimit {
		t.Errorf("%d events allowed at once, want %d", n, HostLimit)
	}
}

func TestFileLimiterFailsOpen(t *testing.T) {
	l := NewFileLimiter(filepath.Join(t.TempDir(), "no", "such", "dir", "rate"))
	if !l.Allow(time.Now()) {
		t.Error("an unusable count file stopped an alert")
	}
}
