//go:build linux

package host

import (
	"bytes"
	"encoding/hex"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
	"golang.org/x/sys/unix"
)

// These real connected Unix endpoints test framing and native ancillary
// retention only. Assigning a transport cursor issues no runtime/OP Source.
func prepareCursorNativePairV1(t *testing.T) (*StartupLineV1, *net.UnixConn) {
	t.Helper()
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	connections := make([]*net.UnixConn, 2)
	for index, fd := range pair {
		file := os.NewFile(uintptr(fd), "original-prepare-cursor-stream")
		conn, err := net.FileConn(file)
		_ = file.Close()
		if err != nil {
			t.Fatal(err)
		}
		connections[index] = conn.(*net.UnixConn)
		t.Cleanup(func() { _ = conn.Close() })
	}
	line, err := NewStartupLineV1(connections[0])
	if err != nil {
		t.Fatal(err)
	}
	line.prepareCursor = &sandboxPrepareFrameCursorV1{}
	t.Cleanup(func() { _ = line.Close() })
	return line, connections[1]
}

func TestSandboxPrepareNativeCursorRetainsSCMAndQueuedReplyAtEveryByteCut(t *testing.T) {
	payload := []byte(`{"id":3,"ok":true}`)
	wire, err := proto.EncodeFrame(proto.TResponse, payload)
	if err != nil {
		t.Fatal(err)
	}
	queued, err := proto.EncodeFrame(proto.TResponse, []byte(`{"id":4,"ok":true}`))
	if err != nil {
		t.Fatal(err)
	}
	for cut := 1; cut < len(wire); cut++ {
		line, sender := prepareCursorNativePairV1(t)
		original, err := os.CreateTemp(t.TempDir(), "original-native-loan")
		if err != nil {
			t.Fatal(err)
		}
		defer original.Close()
		if n, _, err := sender.WriteMsgUnix(wire[:cut], unix.UnixRights(int(original.Fd())), nil); err != nil || n != cut {
			t.Fatalf("cut %d initial native prefix: n=%d, %v", cut, n, err)
		}
		_ = line.SetDeadline(time.Now().Add(5 * time.Millisecond))
		if _, err := line.ReadFrame(); !sandboxPrepareReadDeadlineV1(err) {
			t.Fatalf("cut %d did not retain its deadline: %v", cut, err)
		}
		if len(line.files) != 1 || line.cred == nil || line.cred.pid != os.Getpid() || line.controlAt != line.start {
			t.Fatalf("cut %d lost original SCM role, sender or boundary", cut)
		}
		borrowed := line.files[0]
		if _, err := borrowed.Stat(); err != nil {
			t.Fatalf("cut %d deadline closed its retained native role: %v", cut, err)
		}
		_ = line.SetDeadline(time.Time{})
		if _, err := sender.Write(append(append([]byte(nil), wire[cut:]...), queued...)); err != nil {
			t.Fatal(err)
		}
		frame, err := line.ReadFrame()
		if err != nil || frame.Type != proto.TResponse || !bytes.Equal(frame.Payload, payload) {
			t.Fatalf("cut %d failed to finish the original frame: %+v, %v", cut, frame, err)
		}
		loan, credentials, err := line.takeLoanFile()
		if err != nil || loan != borrowed || credentials.pid != os.Getpid() {
			t.Fatalf("cut %d replaced its original retained native role: %v", cut, err)
		}
		_ = loan.Close()
		frame, err = line.ReadFrame()
		if err != nil || string(frame.Payload) != `{"id":4,"ok":true}` {
			t.Fatalf("cut %d consumed a queued reply or restarted the reader: %+v, %v", cut, frame, err)
		}
	}
}

func TestSandboxPrepareNativeCursorClosesExtraSCMAtResumedBody(t *testing.T) {
	line, sender := prepareCursorNativePairV1(t)
	wire, err := proto.EncodeFrame(proto.TResponse, []byte(`{"id":4,"ok":true}`))
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.CreateTemp(t.TempDir(), "original-native-loan")
	if err != nil {
		t.Fatal(err)
	}
	defer original.Close()
	if _, _, err := sender.WriteMsgUnix(wire[:5], unix.UnixRights(int(original.Fd())), nil); err != nil {
		t.Fatal(err)
	}
	_ = line.SetDeadline(time.Now().Add(5 * time.Millisecond))
	if _, err := line.ReadFrame(); !sandboxPrepareReadDeadlineV1(err) {
		t.Fatal(err)
	}
	borrowed := line.files[0]
	_ = line.SetDeadline(time.Time{})
	if _, _, err := sender.WriteMsgUnix(wire[5:], unix.UnixRights(int(original.Fd())), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := line.ReadFrame(); err == nil || !line.preparePoison || len(line.files) != 0 {
		t.Fatal("a later native role renewed the interrupted frame", err)
	}
	if _, err := borrowed.Stat(); err == nil {
		t.Fatal("protocol failure leaked the earlier retained native role")
	}
	if _, err := line.ReadFrame(); err == nil {
		t.Fatal("a malformed ancillary frame restarted from an empty decoder")
	}
}

func TestSandboxPrepareCursorSenderChildV1(t *testing.T) {
	if os.Getenv("PREPARE_CURSOR_CHILD") != "1" {
		return
	}
	data, err := hex.DecodeString(os.Getenv("PREPARE_CURSOR_BYTES"))
	if err != nil {
		t.Fatal(err)
	}
	file := os.NewFile(3, "same-connected-stream-other-current-sender")
	if _, err := file.Write(data); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
}

func TestSandboxPrepareNativeCursorRejectsChangedCurrentSenderAfterDeadline(t *testing.T) {
	line, sender := prepareCursorNativePairV1(t)
	wire, err := proto.EncodeFrame(proto.TResponse, []byte(`{"id":4,"ok":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sender.Write(wire[:5]); err != nil {
		t.Fatal(err)
	}
	_ = line.SetDeadline(time.Now().Add(5 * time.Millisecond))
	if _, err := line.ReadFrame(); !sandboxPrepareReadDeadlineV1(err) {
		t.Fatal(err)
	}
	duplicate, err := sender.File()
	if err != nil {
		t.Fatal(err)
	}
	defer duplicate.Close()
	child := exec.Command(os.Args[0], "-test.run=^TestSandboxPrepareCursorSenderChildV1$")
	child.Env = append(os.Environ(), "PREPARE_CURSOR_CHILD=1", "PREPARE_CURSOR_BYTES="+hex.EncodeToString(wire[5:]))
	child.ExtraFiles = []*os.File{duplicate}
	if err := child.Run(); err != nil {
		t.Fatal(err)
	}
	_ = line.SetDeadline(time.Time{})
	if _, err := line.ReadFrame(); err == nil || !line.preparePoison {
		t.Fatal("another current native sender renewed the interrupted frame", err)
	}
}

func TestSandboxPrepareNativeCloseErrorsRemainStickyV1(t *testing.T) {
	line, _ := prepareCursorNativePairV1(t)
	if err := line.conn.Close(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := line.Close(); err == nil {
			t.Fatal("an original stream close error became cleanup success")
		}
	}
	pidfd, err := unix.PidfdOpen(os.Getpid(), 0)
	if err != nil {
		t.Fatal(err)
	}
	peer := &nativeStartupPeerV1{pidfd: pidfd}
	if err := unix.Close(pidfd); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := peer.Close(); err == nil {
			t.Fatal("an original pidfd close error became cleanup success")
		}
	}
}
