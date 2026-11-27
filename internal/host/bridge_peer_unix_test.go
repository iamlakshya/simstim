//go:build linux || darwin

package host

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

func TestRunBridgeDuplexHalfCloseDeliversInFlightResponse(t *testing.T) {
	origDrain := bridgeDrain
	origCgroup := SelfCgroup
	origCgroupNS := CgroupNamespaces
	bridgeDrain = 2 * time.Second
	SelfCgroup = func() ([]byte, error) { return []byte("0::/user.slice\n"), nil }
	CgroupNamespaces = func() (string, string, error) { return "", "", nil }
	t.Cleanup(func() {
		bridgeDrain = origDrain
		SelfCgroup = origCgroup
		CgroupNamespaces = origCgroupNS
	})

	runDir, err := os.MkdirTemp("", "brdg-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runDir) })
	runDir, err = filepath.EvalSymlinks(runDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", runDir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(runDir, "cfg"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(runDir, "st"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(runDir, "rt"))

	const id = "abcd2345"
	holderSock := paths.Socket(id)
	if err := os.MkdirAll(filepath.Dir(holderSock), 0o700); err != nil {
		t.Fatal(err)
	}
	holderLn, err := net.Listen("unix", holderSock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = holderLn.Close() })

	reqReceived := make(chan struct{})
	releaseReply := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseReply) }) }
	t.Cleanup(release)
	holderErr := make(chan error, 1)
	go func() {
		conn, acceptErr := holderLn.Accept()
		if acceptErr != nil {
			holderErr <- acceptErr
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		r := bufio.NewReader(conn)
		w := proto.NewWriter(conn)
		if _, readErr := proto.Read(r); readErr != nil {
			holderErr <- readErr
			return
		}
		if writeErr := w.WriteJSON(proto.THello, proto.Hello{
			Magic:    proto.Magic,
			Protocol: version.Protocol,
			Version:  version.Version,
			Role:     "holder",
		}); writeErr != nil {
			holderErr <- writeErr
			return
		}
		if _, readErr := proto.Read(r); readErr != nil {
			holderErr <- readErr
			return
		}
		close(reqReceived)
		<-releaseReply
		holderErr <- w.WriteJSON(proto.TResponse, proto.Response{
			ID:     1,
			OK:     true,
			Result: json.RawMessage(`{"text":"duplex-ok"}`),
		})
	}()

	bridgeSock := filepath.Join(runDir, "bridge.sock")
	bridgeLn, err := net.Listen("unix", bridgeSock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bridgeLn.Close() })

	serverReady := make(chan *net.UnixConn, 1)
	go func() {
		c, acceptErr := bridgeLn.Accept()
		if acceptErr != nil {
			serverReady <- nil
			return
		}
		serverReady <- c.(*net.UnixConn)
	}()

	rawClient, err := net.Dial("unix", bridgeSock)
	if err != nil {
		t.Fatal(err)
	}
	clientConn := rawClient.(*net.UnixConn)
	t.Cleanup(func() { _ = clientConn.Close() })
	serverConn := <-serverReady
	if serverConn == nil {
		t.Fatal("bridge listener failed to accept duplex Unix connection")
	}
	t.Cleanup(func() { _ = serverConn.Close() })

	bridgeDone := make(chan error, 1)
	go func() { bridgeDone <- RunBridge(serverConn, serverConn) }()

	_ = clientConn.SetDeadline(time.Now().Add(5 * time.Second))
	cr := bufio.NewReader(clientConn)
	cw := proto.NewWriter(clientConn)

	if err := cw.WriteJSON(proto.THello, proto.Hello{
		Magic:    proto.Magic,
		Protocol: version.Protocol,
		Version:  version.Version,
		Role:     "client",
	}); err != nil {
		t.Fatal(err)
	}

	helloFrame, err := proto.Read(cr)
	if err != nil || helloFrame.Type != proto.THello {
		t.Fatalf("bridge hello = (%+v, %v), want THello", helloFrame, err)
	}

	if err := cw.WriteJSON(proto.TRequest, proto.Request{
		ID:   7,
		Op:   "peek",
		Args: json.RawMessage(`{"id":"abcd2345","bytes":16}`),
	}); err != nil {
		t.Fatal(err)
	}

	select {
	case <-reqReceived:
	case <-time.After(3 * time.Second):
		t.Fatal("mock holder did not receive peek request from bridge")
	}

	// Half-close the client write side on this O_RDWR duplex socket while the
	// bridge is still waiting to send the response. With directional watches,
	// outWatch watches write-peer-gone (not read-EOF) and must not call b.shut().
	if err := clientConn.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	release()

	respFrame, err := proto.Read(cr)
	if err != nil || respFrame.Type != proto.TResponse {
		t.Fatalf("response after client CloseWrite = (%+v, %v), want TResponse", respFrame, err)
	}
	var resp proto.Response
	if err := json.Unmarshal(respFrame.Payload, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.ID != 7 || !resp.OK || !strings.Contains(string(resp.Result), "duplex-ok") {
		t.Fatalf("unexpected peek response after CloseWrite: %+v", resp)
	}

	if err := <-holderErr; err != nil {
		t.Fatalf("mock holder error: %v", err)
	}
	select {
	case err := <-bridgeDone:
		if err != nil {
			t.Fatalf("RunBridge returned %v, want nil", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("RunBridge did not finish cleanly after half-closed EOF")
	}
}
