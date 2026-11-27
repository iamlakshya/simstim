//go:build linux

package slotlaunch

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"time"

	"golang.org/x/sys/unix"
)

type sourceGuestV1 struct {
	net.Conn
	source *NativeSourceV1
}

func (g *sourceGuestV1) Close() error {
	err := g.Conn.Close()
	g.source.mu.Lock()
	defer g.source.mu.Unlock()
	if g.source.guest == g {
		g.source.guest = nil
		if g.source.guestPeer != nil {
			_ = g.source.guestPeer.Close()
			g.source.guestPeer = nil
		}
	}
	return err
}

// DialAPIV1 captures a second socket owned by the SAME native VMM. Only the
// fixed state/power-button client in the engine uses this endpoint.
func (s *NativeSourceV1) DialAPIV1(ctx context.Context) (net.Conn, error) {
	if s == nil {
		return nil, refused("the native VMM source is absent")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(ctx, s.request); err != nil {
		return nil, err
	}
	connection, err := (&net.Dialer{}).DialContext(ctx, "unix", RootPath()+"/vms/"+s.request.Session+"/api.sock")
	if err != nil {
		return nil, err
	}
	local, ok := connection.(*net.UnixConn)
	if !ok {
		connection.Close()
		return nil, refused("the VMM control endpoint is not a Unix stream")
	}
	file, err := local.File()
	if err != nil {
		connection.Close()
		return nil, err
	}
	defer file.Close()
	peer, err := s.inspectNativeGuest(ctx, file)
	if err != nil {
		connection.Close()
		return nil, err
	}
	defer peer.Close()
	actual, err := nativePID(peer)
	if err != nil {
		connection.Close()
		return nil, err
	}
	if s.guestPeer == nil {
		connection.Close()
		return nil, refused("the VMM control source has no original guest peer")
	}
	expected, err := nativePID(s.guestPeer)
	if err != nil || actual != expected || !pidLive(s.guestPeer) {
		connection.Close()
		return nil, refused("the VMM control endpoint belongs to another native process")
	}
	return connection, nil
}

// DialGuestV1 creates the actual fixed private endpoint connection and asks
// the root inspector to capture that SAME socket's native peer. It accepts
// no pathname, process ID, replacement socket or verification callback.
func (s *NativeSourceV1) DialGuestV1(ctx context.Context) (net.Conn, error) {
	if s == nil {
		return nil, refused("the native guest source is missing")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(ctx, s.request); err != nil {
		return nil, err
	}
	if s.guest != nil {
		return nil, refused("the native guest source already owns its guest connection")
	}
	connection, err := (&net.Dialer{}).DialContext(ctx, "unix", RootPath()+"/vms/"+s.request.Session+"/vsock.sock")
	if err != nil {
		return nil, err
	}
	failed := true
	defer func() {
		if failed {
			connection.Close()
		}
	}()
	unixConnection, ok := connection.(*net.UnixConn)
	if !ok {
		return nil, refused("the native guest endpoint is not a Unix stream")
	}
	file, err := unixConnection.File()
	if err != nil {
		return nil, err
	}
	defer file.Close()
	peer, err := s.inspectNativeGuest(ctx, file)
	if err != nil {
		return nil, err
	}
	guest := &sourceGuestV1{Conn: connection, source: s}
	s.guest, s.guestPeer = guest, peer
	failed = false
	return guest, nil
}

func (s *NativeSourceV1) inspectNativeGuest(ctx context.Context, guest *os.File) (_ *os.File, returned error) {
	launcher, err := fixedLauncher()
	if err != nil {
		return nil, err
	}
	defer launcher.Close()
	actualLauncher, err := launcher.Stat()
	if err != nil {
		return nil, err
	}
	originalLauncher, err := s.files[11].Stat()
	if err != nil || !os.SameFile(actualLauncher, originalLauncher) {
		return nil, refused("the original root launcher source changed before inspecting its guest")
	}
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	parent, child := os.NewFile(uintptr(pair[0]), "guest-native-source"), os.NewFile(uintptr(pair[1]), "root-native-inspection")
	defer parent.Close()
	defer child.Close()
	if err := unix.SetsockoptInt(pair[0], unix.SOL_SOCKET, unix.SO_PASSCRED, 1); err != nil {
		return nil, err
	}
	connection, err := net.FileConn(parent)
	if err != nil {
		return nil, err
	}
	defer connection.Close()
	socket := connection.(*net.UnixConn)
	command := exec.CommandContext(ctx, "/usr/bin/sudo", "-n", LauncherPath())
	command.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C"}
	command.Stdin, command.Stdout, command.Stderr = child, child, io.Discard
	if err := command.Start(); err != nil {
		return nil, err
	}
	child.Close()
	waited := false
	defer func() {
		if !waited {
			command.Process.Kill()
			command.Wait()
		}
	}()
	if err := socket.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		return nil, err
	}
	if err := json.NewEncoder(socket).Encode(Request{Op: "native", Session: s.request.Session}); err != nil {
		return nil, err
	}
	var ack [1]byte
	if _, err := io.ReadFull(socket, ack[:]); err != nil || ack[0] != 1 {
		return nil, refused("the original root inspector did not accept its descriptor channel")
	}
	raw, err := socket.SyscallConn()
	if err != nil {
		return nil, err
	}
	var sent error
	if err := raw.Write(func(fd uintptr) bool {
		sent = unix.Sendmsg(int(fd), []byte{1}, unix.UnixRights(int(guest.Fd()), int(s.files[1].Fd()), int(s.files[2].Fd()), int(s.files[3].Fd())), nil, unix.MSG_NOSIGNAL)
		return sent != unix.EAGAIN
	}); err != nil {
		return nil, err
	}
	if sent != nil {
		return nil, sent
	}
	response, files, credential, err := receiveNative(socket)
	retained := false
	defer func() {
		for i, file := range files {
			if i != 0 || !retained {
				file.Close()
			}
		}
	}()
	if err != nil {
		return nil, err
	}
	if !response.OK || len(files) != 6 || credential == nil || credential.Uid != 0 {
		return nil, refused("the actual root guest inspector returned an incomplete native vector")
	}
	sender, err := nativePID(files[5])
	if err != nil || sender != int(credential.Pid) || !pidLive(files[5]) {
		return nil, refused("the guest inspector sender is not its current kernel root credential")
	}
	rootExe, err := files[4].Stat()
	if err != nil || !os.SameFile(rootExe, originalLauncher) {
		return nil, refused("another executable answered the native guest inspection")
	}
	actualExe, err := files[1].Stat()
	if err != nil {
		return nil, err
	}
	expectedExe, err := s.files[3].Stat()
	if err != nil || !os.SameFile(actualExe, expectedExe) {
		return nil, refused("the guest peer executed another original VMM object")
	}
	actualGroup, err := files[2].Stat()
	if err != nil {
		return nil, err
	}
	expectedGroup, err := s.files[2].Stat()
	if err != nil || !os.SameFile(actualGroup, expectedGroup) {
		return nil, refused("the guest peer has another original cgroup object")
	}
	actualInit, err := nativePID(files[3])
	if err != nil {
		return nil, err
	}
	expectedInit, err := nativePID(s.files[1])
	if err != nil || actualInit != expectedInit || !pidLive(files[3]) || !pidLive(s.files[1]) {
		return nil, refused("the guest peer has another original namespace init")
	}
	peer, err := nativePID(files[0])
	if err != nil || !pidLive(files[0]) {
		return nil, refused("the inspected native guest peer ended")
	}
	localPeerFD, err := unix.GetsockoptInt(int(guest.Fd()), unix.SOL_SOCKET, unix.SO_PEERPIDFD)
	if err != nil {
		return nil, err
	}
	localPeer := os.NewFile(uintptr(localPeerFD), "same-native-guest-peer")
	defer localPeer.Close()
	localPID, err := nativePID(localPeer)
	if err != nil || localPID != peer || !pidLive(localPeer) {
		return nil, refused("the root inspector captured another guest socket's peer")
	}
	if _, err := socket.Write([]byte{1}); err != nil {
		return nil, err
	}
	err = command.Wait()
	waited = true
	if err != nil {
		return nil, refused("the original root guest inspector did not finish its native handoff")
	}
	retained = true
	return files[0], nil
}

// SameGuestV1 checks the exact connection this source actually created. The
// engine separately binds its own Start-returned wrapper and protected Need.
func (s *NativeSourceV1) SameGuestV1(ctx context.Context, connection net.Conn) error {
	if s == nil {
		return refused("the native guest source is absent")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(ctx, s.request); err != nil {
		return err
	}
	guest, ok := connection.(*sourceGuestV1)
	if !ok || guest != s.guest || guest.source != s || s.guestPeer == nil || !pidLive(s.guestPeer) {
		return refused("the native guest connection is not the original source-owned stream")
	}
	return nil
}
