//go:build linux

package host

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/supply"
	"golang.org/x/sys/unix"
)

func requireOriginalPurgeCloseProfileV4(t *testing.T) {
	t.Helper()
	runtime.LockOSThread()
	err := supply.CheckFileCloseThreadV1()
	runtime.UnlockOSThread()
	if err != nil {
		t.Skipf("native first-close profile unavailable: %v", err)
	}
}

func TestPurgeOriginalFactoryKeepsUnknownExternalFirstCloseV4(t *testing.T) {
	requireOriginalPurgeCloseProfileV4(t)
	for _, external := range []bool{false, true} {
		t.Run(map[bool]string{false: "original", true: "external"}[external], func(t *testing.T) {
			file, err := os.CreateTemp(t.TempDir(), "code")
			if err != nil {
				t.Fatal(err)
			}
			owner := &purgeAttemptCodeV2{source: file, loans: map[*purgeAttemptCodeLoanV2]struct{}{}}
			if external {
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
			}
			closed := owner.Close()
			native := owner.checkNativeRetiredV3(context.Background())
			if external {
				if closed == nil || native == nil || owner.closed {
					t.Fatalf("external close became factory retirement: close=%v native=%v", closed, native)
				}
				if owner.Close() == nil || owner.checkNativeRetiredV3(context.Background()) == nil {
					t.Fatal("a second close manufactured a native factory receipt")
				}
			} else if closed != nil || native != nil || owner.Close() != nil {
				t.Fatalf("original factory close did not retain native facts: close=%v native=%v", closed, native)
			}
		})
	}
}

func TestPurgeOriginalDomainRetainsUnknownRootV4(t *testing.T) {
	requireOriginalPurgeCloseProfileV4(t)
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	owner := &purgeAttemptDomainV3{control: root}
	if owner.Close() == nil || owner.checkNativeRetiredV3(context.Background()) == nil {
		t.Fatal("an externally closed Root became an original domain retirement")
	}
	if owner.Close() == nil || owner.checkNativeRetiredV3(context.Background()) == nil {
		t.Fatal("retry erased the original unknown Root fact")
	}
}

func TestPurgePartialCodeRootClosesWithoutDirectoryIdentityGrantV4(t *testing.T) {
	requireOriginalPurgeCloseProfileV4(t)
	path := t.TempDir()
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	owner := &purgeAttemptCodeV2{directory: root, directoryCreated: true, loans: map[*purgeAttemptCodeLoanV2]struct{}{}}
	if owner.Close() == nil {
		t.Fatal("missing original directory identity authorised namespace cleanup")
	}
	if err := owner.retirement.checkRootsV4([]*os.Root{root}); err != nil {
		t.Fatalf("partial capture dropped its actual owned Root: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("unknown named data was not preserved: %v", err)
	}
	if owner.checkNativeRetiredV3(context.Background()) == nil {
		t.Fatal("partial capture became a complete factory cleanup receipt")
	}
}

func TestPurgeOwnedListenerCloseJoinsOriginalAcceptV4(t *testing.T) {
	requireOriginalPurgeCloseProfileV4(t)
	path := t.TempDir()
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(path, "attempt.sock"), Net: "unix"})
	if err != nil {
		_ = root.Close()
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	info, err := root.Lstat("attempt.sock")
	if err != nil {
		t.Fatal(err)
	}
	file, err := root.OpenFile("attempt.sock", unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	reference, err := cachefs.CaptureRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	owner := &purgeOwnedListenerV3{root: root, rootRef: reference, listener: listener, socket: info, socketFile: file}
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		connection, _ := owner.acceptV4()
		if connection != nil {
			_ = connection.Close()
		}
	}()
	closeDone := make(chan error, 1)
	go func() { closeDone <- owner.Close() }()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first listener close did not interrupt and join its actual Accept")
	}
	select {
	case <-acceptDone:
	case <-time.After(2 * time.Second):
		t.Fatal("native retirement preceded the actual Accept join")
	}
	if err := owner.checkNativeRetiredV3(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := owner.Close(); err != nil {
		t.Fatalf("same first-close facts were lost on retry: %v", err)
	}
}
