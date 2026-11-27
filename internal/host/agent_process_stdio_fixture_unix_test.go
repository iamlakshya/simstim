//go:build !windows

package host

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/agentbridge"
	"github.com/iamlakshya/simstim/internal/link"
)

func agentProcessAbortUnix(ctx context.Context, cancel context.CancelFunc, path string) int {
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return 91
	}
	listener.SetUnlinkOnClose(false)
	info, err := os.Lstat(path)
	if err != nil {
		return 92
	}
	defer func() {
		_ = listener.Close()
		if current, err := os.Lstat(path); err == nil && os.SameFile(current, info) {
			_ = os.Remove(path)
		}
	}()
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return 93
	}
	bridge := &hostAgentBridge{ctx: ctx, cancel: cancel, processOnly: true, clients: map[net.Conn]struct{}{}}
	bridge.endpoint = &hostAgentEndpoint{listener: listener, root: root, name: filepath.Base(path), info: info, witness: "absent-witness"}
	bridge.conn = agentbridge.NewProcessConn(os.Stdin, os.Stdout)
	bridge.link = link.New(bridge.conn, link.Guest, agentbridge.Options())
	openCtx, stopOpen := context.WithTimeout(ctx, agentbridge.HandshakeTimeout)
	_, err = bridge.link.Open(openCtx, link.KindAgent, agentbridge.Scope{Session: "abc23def", Lease: strings.Repeat("a", 32)})
	stopOpen()
	if err != nil {
		return 94
	}
	var peers []net.Conn
	defer func() {
		for _, peer := range peers {
			_ = peer.Close()
		}
	}()
	for range agentbridge.MaxStreams {
		peer, err := net.Dial("unix", path)
		if err != nil {
			return 95
		}
		server, err := listener.AcceptUnix()
		if err != nil {
			_ = peer.Close()
			return 96
		}
		peers = append(peers, peer)
		bridge.clients[server] = struct{}{}
		bridge.workers.Add(1)
		go func() { defer bridge.workers.Done(); _, _ = io.Copy(io.Discard, server) }()
	}
	// Raw link stdout is full, but all real Unix workers must close/join.
	if err := bridge.finish(); err != nil {
		return 101
	}
	select {
	case <-bridge.link.Done():
	default:
		return 97
	}
	for _, peer := range peers {
		_ = peer.SetReadDeadline(time.Now().Add(time.Second))
		var byte [1]byte
		if _, err := peer.Read(byte[:]); !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
			return 98
		}
	}
	status := os.NewFile(3, "owned-agent-test-status")
	if status == nil {
		return 99
	}
	defer status.Close()
	if _, err := status.Write([]byte{1}); err != nil {
		return 100
	}
	return 0
}
