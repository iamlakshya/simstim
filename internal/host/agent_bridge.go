package host

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/agentbridge"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

// Agent-only bridges have their own registry. Their transport close never
// calls Session.Close from a link reader or writer callback.
var liveAgentBridges sync.Map

type hostAgentBridge struct {
	ctx    context.Context
	cancel context.CancelFunc
	conn   *agentbridge.Conn
	link   *link.Session
	done   chan struct{}
	once   sync.Once

	mu             sync.Mutex
	scope          agentbridge.Scope
	bindDeadline   time.Time
	collectBinding bool
	probeStarted   bool
	probe          net.Conn
	probeDial      func(context.Context, string) (net.Conn, error)
	endpoint       *hostAgentEndpoint
	active         bool
	closed         bool
	clients        map[net.Conn]struct{}
	slots          chan struct{}
	workers        sync.WaitGroup
	processOnly    bool
	cleanupCtx     context.Context
	stopCleanup    context.CancelFunc
}

// RunAgentBridge serves an explicitly owned interruptible library
// transport. The fixed helper process uses RunAgentBridgeProcess instead.
// Both bound their initial reader before accepting a peer frame.
func RunAgentBridge(in io.Reader, out io.Writer) error {
	return runAgentBridgeEntry(context.Background(), in, out, false)
}

func runAgentBridgeEntry(parent context.Context, in io.Reader, out io.Writer, processOnly bool) error {
	closeTransport := func() error { return nil }
	var err error
	if !processOnly {
		closeTransport, err = agentStdioCloser(in, out)
	}
	if err != nil {
		return err
	}
	owned := agentbridge.NewConn(in, out, closeTransport)
	if processOnly {
		owned = agentbridge.NewProcessConn(in, out)
	}
	defer owned.Close()
	identityCtx, identityCancel := context.WithTimeout(parent, agentbridge.HandshakeTimeout)
	defer identityCancel()
	timeout := context.AfterFunc(identityCtx, func() { _ = owned.Close() })
	defer timeout()
	reader := bufio.NewReaderSize(owned, agentbridge.MaxIdentityHello)
	frame, err := readAgentProcessHello(identityCtx, reader, processOnly)
	if parent.Err() != nil {
		return parent.Err()
	}
	if err != nil || frame.Type != proto.THello {
		return UserError("ssh_agent_refused", "the agent bridge did not receive a bounded identity hello",
			"Update the client, then attach again.")
	}
	hello, err := agentbridge.DecodeHello(frame.Payload)
	if err != nil || hello.Role != agentbridge.Role || hello.Check(version.Protocol) != nil {
		return UserError("ssh_agent_refused", "the agent bridge identity hello is not valid for this mode",
			"Update the client, then attach again.")
	}
	hostID, err := readAgentProcessHostID(identityCtx, processOnly)
	if err != nil {
		return UserError("host_id", "the agent bridge cannot read the host identity",
			"Check the host's private state folder, then attach again.")
	}
	writer := proto.NewWriter(owned)
	if err := writeAgentProcessHello(identityCtx, writer, proto.Hello{Magic: proto.Magic, Protocol: version.Protocol,
		Version: version.Version, Role: "bridge", HostID: hostID, Features: []string{agentbridge.Feature}}, processOnly); err != nil {
		return err
	}
	timeout()
	return runAgentBridgeMode(parent, reader, owned, owned, processOnly)
}

func runAgentBridgeMode(parent context.Context, reader *bufio.Reader, in io.Reader, out io.Writer, processOnly bool) (result error) {
	closeTransport := func() error { return nil }
	var err error
	if !processOnly {
		closeTransport, err = agentStdioCloser(in, out)
	}
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(parent)
	b := &hostAgentBridge{ctx: ctx, cancel: cancel, done: make(chan struct{}),
		clients: map[net.Conn]struct{}{}, slots: make(chan struct{}, agentbridge.MaxStreams)}
	b.processOnly = processOnly
	b.conn = agentbridge.NewConn(reader, out, closeTransport)
	if processOnly {
		b.conn = agentbridge.NewProcessConn(reader, out)
	}
	b.link = link.New(b.conn, link.Guest, agentbridge.Options())
	stopParent := context.AfterFunc(parent, b.revoke)
	defer stopParent()
	if !processOnly {
		liveAgentBridges.Store(b, true)
		defer liveAgentBridges.Delete(b)
	}
	defer close(b.done)
	defer func() {
		if cleanup := b.finish(); cleanup != nil {
			if processOnly {
				result = &agentProcessCleanupError{cause: cleanup}
			} else {
				result = errors.Join(result, cleanup)
			}
		}
	}()
	if processOnly {
		// Native filesystem calls are not universally cancellable. The
		// fixed main must observe parent/link loss independently, revoke
		// capabilities, and reach its one cleanup budget before process exit.
		result := make(chan error, 1)
		b.workers.Add(1)
		go func() {
			defer b.workers.Done()
			result <- b.run()
		}()
		select {
		case err = <-result:
		case <-parent.Done():
			err = parent.Err()
		case <-b.link.Done():
			err = b.link.Err()
		}
	} else {
		err = b.run()
	}
	if errors.Is(err, link.ErrLinkClosed) || errors.Is(err, net.ErrClosed) || errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func agentStdioCloser(in io.Reader, out io.Writer) (func() error, error) {
	// The fixed process entry owns inherited stdio. Library callers must
	// supply their actual owned transport, never a generic bridge watcher.
	if _, ok := in.(*watched); ok {
		return nil, agentLibraryTransportError()
	}
	// Known os.File values need actual runtime deadline support. Merely
	// implementing io.Closer does not make inherited blocking stdio safe.
	if file, ok := in.(*os.File); ok {
		if err := file.SetReadDeadline(time.Time{}); err != nil {
			return nil, agentLibraryTransportError()
		}
	}
	if file, ok := out.(*os.File); ok {
		if err := file.SetWriteDeadline(time.Time{}); err != nil {
			return nil, agentLibraryTransportError()
		}
	}
	// net.Conn.Close has an interruption contract. Other custom closers
	// do not prove that a blocked reader or writer can return. Pollable
	// files were checked through their real runtime deadline support above.
	if _, socket := in.(net.Conn); !socket {
		if _, file := in.(*os.File); !file {
			return nil, agentLibraryTransportError()
		}
	}
	if _, socket := out.(net.Conn); !socket {
		if _, file := out.(*os.File); !file {
			return nil, agentLibraryTransportError()
		}
	}
	input, inputOK := in.(io.Closer)
	output, outputOK := out.(io.Closer)
	if !inputOK || !outputOK {
		return nil, agentLibraryTransportError()
	}
	return func() error { return errors.Join(input.Close(), output.Close()) }, nil
}

func agentLibraryTransportError() error {
	return UserError("ssh_agent_refused", "the agent library transport cannot be interrupted safely", "Use the fixed process-only agent entry over SSH.")
}

func (b *hostAgentBridge) run() error {
	ctx, cancel := context.WithTimeout(b.ctx, agentbridge.HandshakeTimeout)
	defer cancel()
	control, err := b.link.Accept(ctx)
	if err != nil {
		return err
	}
	if control.Kind() != link.KindCtl {
		return agentControlError()
	}
	var header agentbridge.ControlHeader
	if err := control.Header(&header); err != nil || header.Kind != string(link.KindCtl) {
		return agentControlError()
	}
	b.scope = header.Scope
	b.collectBinding = header.CollectBinding
	b.bindDeadline = time.Now().Add(agentbridge.HandshakeTimeout)
	if err := control.SetDeadline(b.bindDeadline); err != nil {
		return err
	}
	if err := b.controlAction(control, "bind", 1, b.bindDeadline); err != nil {
		return err
	}
	preparedDeadline := time.Now().Add(agentbridge.PreparedTimeout)
	if err := control.SetDeadline(preparedDeadline); err != nil {
		return err
	}
	if err := b.controlAction(control, "activate", 2, preparedDeadline); err != nil {
		return err
	}
	for seq := uint64(3); ; seq++ {
		deadline := time.Now().Add(agentbridge.LeaseTimeout)
		if err := control.SetDeadline(deadline); err != nil {
			return err
		}
		if err := b.controlAction(control, "ping", seq, deadline); err != nil {
			return err
		}
		if seq == ^uint64(0) {
			return agentControlError()
		}
	}
}

func agentControlError() error {
	return UserError("ssh_agent_refused", "the agent bridge control does not match its session and lease",
		"Detach, then attach again with the current client.")
}

func (b *hostAgentBridge) controlAction(control *link.Stream, action string, seq uint64, deadline time.Time) error {
	var request agentbridge.Request
	if err := agentbridge.ReadMessage(control, &request); err != nil {
		return err
	}
	if request.Scope != b.scope || request.Action != action || request.Seq != seq || request.Seq == 0 {
		return agentControlError()
	}
	var err error
	switch action {
	case "bind":
		err = b.bind()
	case "activate":
		err = b.activate()
	case "ping":
		// Its exact scope and next sequence were checked above.
	default:
		err = agentControlError()
	}
	reply := agentbridge.Reply{Scope: b.scope, Action: action, Seq: seq, OK: err == nil}
	if err != nil {
		value := attachError(err)
		reply.Error = &value
	}
	if writeErr := agentbridge.WriteMessage(control, reply); writeErr != nil {
		return writeErr
	}
	flushCtx, stopFlush := context.WithDeadline(b.ctx, deadline)
	defer stopFlush()
	if flushErr := control.Flush(flushCtx); flushErr != nil {
		return flushErr
	}
	return err
}

// endAgentBridges revokes every capability before joining any cleanup.
func endAgentBridges() {
	var bridges []*hostAgentBridge
	liveAgentBridges.Range(func(key, _ any) bool {
		b := key.(*hostAgentBridge)
		bridges = append(bridges, b)
		b.revoke()
		return true
	})
	timeout := time.NewTimer(agentbridge.LeaseTimeout)
	defer timeout.Stop()
	for _, b := range bridges {
		select {
		case <-b.done:
		case <-timeout.C:
			return
		}
	}
}
