//go:build unix

package client

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestDoctorSnapshotDoesNotBlockOnAReplacedFIFO(t *testing.T) {
	candidate, _ := capturedOrphanConfig(t)
	previous := beforeSSHConfigSnapshotOpen
	beforeSSHConfigSnapshotOpen = func(path string) {
		if err := os.Remove(path); err != nil {
			t.Error(err)
			return
		}
		if err := unix.Mkfifo(path, 0o600); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { beforeSSHConfigSnapshotOpen = previous })
	done := make(chan bool, 1)
	go func() { _, ok := snapshotGeneratedSSHConfig(candidate.path); done <- ok }()
	select {
	case accepted := <-done:
		if accepted {
			t.Fatal("doctor accepted a FIFO as its owned regular config")
		}
	case <-time.After(time.Second):
		// Release a regressed blocking open before reporting the failure.
		f, err := os.OpenFile(candidate.path, os.O_WRONLY|unix.O_NONBLOCK, 0)
		if err == nil {
			_ = f.Close()
		}
		t.Fatal("doctor blocked on a FIFO swapped in after its Lstat")
	}
}

func TestCancelledLiveControlProbeCannotCreateAnUnlinkFix(t *testing.T) {
	path := indexedControlPath(t, "box", strings.Repeat("k", 26))
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	previous := probeOwnedControl
	probeOwnedControl = func(ctx context.Context, path string) error {
		cancel()
		return previous(ctx, path)
	}
	t.Cleanup(func() { probeOwnedControl = previous })
	fixes := controlSocketFix(ctx)
	if len(fixes) != 0 {
		t.Fatalf("cancelled probe called a live socket stale: %+v", fixes)
	}
	probeOwnedControl = previous
	// A fresh compatibility call must not inherit a stale unlink plan.
	for _, fix := range LocalFixes(true, []string{"control-sockets"}) {
		if fix.ID == "control-sockets" {
			t.Fatalf("a live socket produced a later unlink fix: %+v", fix)
		}
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("cancelled live probe removed its socket: %v", err)
	}
}

func TestLocalFixesContextReportsCancellationFromAnEmptyProbeResult(t *testing.T) {
	ownLockFiles(t, time.Millisecond, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := LocalFixesContext(ctx, true, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled local fixes = %v", err)
	}
}

func TestCancelledCapturedControlFixCannotUnlink(t *testing.T) {
	path := indexedControlPath(t, "box", strings.Repeat("k", 26))
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	_ = listener.Close()
	fixes := controlSocketFix(context.Background())
	if len(fixes) != 1 {
		t.Fatalf("dead socket fixes = %d", len(fixes))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := fixes[0].run(ctx, &Fix{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled captured fix = %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("cancelled captured fix unlinked its socket: %v", err)
	}
}

func TestControlProbeCancellationErrorsNeverMeanStale(t *testing.T) {
	for name, cause := range map[string]error{"cancelled": context.Canceled, "deadline": context.DeadlineExceeded} {
		t.Run(name, func(t *testing.T) {
			path := indexedControlPath(t, "box", strings.Repeat("k", 26))
			listener, err := net.Listen("unix", path)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			previous := probeOwnedControl
			probeOwnedControl = func(context.Context, string) error {
				return fmt.Errorf("interrupted probe: %w", cause)
			}
			t.Cleanup(func() { probeOwnedControl = previous })
			if fixes := controlSocketFix(context.Background()); len(fixes) != 0 {
				t.Fatalf("probe cancellation was treated as stale: %+v", fixes)
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("interrupted probe removed a live socket: %v", err)
			}
		})
	}
}
