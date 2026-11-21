//go:build linux

package host

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"golang.org/x/sys/unix"
)

func TestPurgeRelayKeepsSameTargetThroughNativeUnlinkV3(t *testing.T) {
	if err := CheckCaller(); err != nil {
		t.Skipf("native outside caller unavailable: %v", err)
	}
	image, err := captureOwnPurgeRelayImageV3()
	if err != nil {
		t.Fatal(err)
	}
	defer image.Close()
	path, err := os.MkdirTemp("/tmp", "pr-target-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(path) })
	home, err := cachefs.OpenPrivateRoot(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer home.Close()
	if err := home.Mkdir("attempt", 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := home.OpenRoot("attempt")
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	directory, err := root.Open(".")
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	info, err := directory.Stat()
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(path, "attempt", "attempt.sock"), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	defer listener.Close()
	if err := root.Chmod("attempt.sock", 0o600); err != nil {
		t.Fatal(err)
	}
	socket, err := root.OpenFile("attempt.sock", unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Close()
	socketInfo, err := socket.Stat()
	if err != nil {
		t.Fatal(err)
	}
	homeRef, err := nativeWorktreeDirectoryRefV1(home)
	if err != nil {
		t.Fatal(err)
	}
	rootRef, err := nativeWorktreeDirectoryRefV1(root)
	if err != nil {
		t.Fatal(err)
	}
	target := &purgeRelayTargetV3{image: image, home: home, root: root, homeRef: homeRef, rootRef: rootRef, rootInfo: info, socketInfo: socketInfo, directory: directory, socket: socket, leaf: "attempt"}
	if err := target.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := cachefs.RenameOwnedDirectoryNoReplace(home, "attempt", "captured"); err != nil {
		t.Fatal(err)
	}
	if err := home.Mkdir("attempt", 0o700); err != nil {
		t.Fatal(err)
	}
	replacement, err := home.Lstat("attempt")
	if err != nil {
		t.Fatal(err)
	}
	if err := target.Check(context.Background()); err == nil {
		t.Fatal("a replacement private directory satisfied the retained target")
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if err := root.Remove("attempt.sock"); err != nil {
		t.Fatal(err)
	}
	if err := home.Remove("captured"); err != nil {
		t.Fatal(err)
	}
	if err := target.Check(context.Background()); err != nil {
		t.Fatal("SAME retained terminal target observations did not survive own unlink", err)
	}
	current, err := home.Lstat("attempt")
	if err != nil || !os.SameFile(current, replacement) {
		t.Fatal("target validation changed the replacement", err)
	}
}
