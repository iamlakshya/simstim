package lima

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"
)

// hostAgentLog waits for the host agent's last line only while it can
// still come.
func TestHostAgentLog(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("signal 0 does not tell whether a process runs on Windows")
	}
	oldWait, oldGrace := haWait, haGrace
	t.Cleanup(func() { haWait, haGrace = oldWait, oldGrace })
	haWait, haGrace = 2*time.Second, 300*time.Millisecond

	ctx := context.Background()
	// A process that has ended.
	done := exec.CommandContext(ctx, "true")
	if err := done.Run(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, pid, log string
		least, most    time.Duration
	}{
		{"a fatal line", strconv.Itoa(os.Getpid()), `{"level":"fatal","msg":"x"}` + "\n", 0, 200 * time.Millisecond},
		{"the host agent has ended", strconv.Itoa(done.Process.Pid), `{"level":"info","msg":"x"}` + "\n", 0, 200 * time.Millisecond},
		{"no ha.pid", "", "", 300 * time.Millisecond, time.Second},
		{"the host agent still runs", strconv.Itoa(os.Getpid()), "", 2 * time.Second, 3 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.pid != "" {
				if err := os.WriteFile(filepath.Join(dir, "ha.pid"), []byte(tc.pid+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(dir, "ha.stderr.log"), []byte(tc.log), 0o644); err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			if got := hostAgentLog(ctx, dir); got != tc.log {
				t.Errorf("log = %q, want %q", got, tc.log)
			}
			if took := time.Since(start); took < tc.least || took > tc.most {
				t.Errorf("took %v, want %v to %v", took, tc.least, tc.most)
			}
		})
	}
	// A cancel ends the wait at once.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ha.pid"), []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		t.Fatal(err)
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	start := time.Now()
	hostAgentLog(cctx, dir)
	if took := time.Since(start); took > 500*time.Millisecond {
		t.Errorf("a cancelled wait took %v", took)
	}
}
