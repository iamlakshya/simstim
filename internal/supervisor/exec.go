package supervisor

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/guest"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

// handleExecConn runs the supervisor side of a streaming "exec" op (isolation
// spec 6.11, 10.2, 10.3, 10.5). After writing an OK TResponse, the session
// socket connection switches to an internal/link session (BridgeOpener,
// AllowVersionMismatch) carrying one KindExec stream spliced to the guest's
// KindExec stream. Closing the client's stream immediately closes the guest's
// stream so guest init kills the running command.
func (s *Supervisor) handleExecConn(conn net.Conn, r *bufio.Reader, w *proto.Writer, req proto.Request) {
	fail := func(code, msg, hint string) {
		_ = w.WriteJSON(proto.TResponse, proto.Response{
			ID:    req.ID,
			Error: &proto.Error{Code: code, Message: msg, Hint: hint},
		})
	}
	var a api.ExecArgs
	if err := json.Unmarshal(req.Args, &a); err != nil || len(a.Argv) == 0 {
		fail("bad_request", "exec requires a command", "Pass a command after --.")
		return
	}
	s.mu.Lock()
	if s.ended || s.stopping {
		s.mu.Unlock()
		fail("sandbox_stopped", "the sandbox is stopped", "Wake the sandbox with resume, then try again.")
		return
	}
	cwd := a.Cwd
	if cwd == "" {
		cwd = s.m.Cwd
	}
	s.mu.Unlock()

	execID := a.ExecID
	if execID == "" {
		execID = host.NewID()
	}
	timeoutMS := a.TimeoutMS
	if timeoutMS <= 0 && !a.NoTimeout {
		timeoutMS = api.DefaultExecTimeoutMS
	}

	ctlCtx, ctlCancel := context.WithTimeout(context.Background(), 10*time.Second)
	resp, err := s.ctlCall(ctlCtx, "exec", guest.ExecReq{
		ID:        execID,
		Argv:      a.Argv,
		Cwd:       cwd,
		TimeoutMS: timeoutMS,
	})
	ctlCancel()
	if err != nil {
		fail("sandbox_stopped", "the sandbox did not answer exec: "+err.Error(), "Check ls and resume the sandbox if stopped.")
		return
	}
	if !resp.OK {
		ce := cleanGuestError(resp.Error, "exec_failed", "guest exec failed", "Try again.")
		fail(ce.Code, ce.Message, ce.Hint)
		return
	}

	openCtx, openCancel := context.WithTimeout(context.Background(), 10*time.Second)
	guestSt, err := s.link.Open(openCtx, link.KindExec, guest.ExecHeader{ID: execID})
	openCancel()
	if err != nil {
		fail("exec_failed", "could not open guest exec stream: "+err.Error(), "Try again.")
		return
	}
	defer func() { _ = guestSt.Close() }()

	resBytes, _ := json.Marshal(api.ExecResponse{ExecID: execID})
	if err := w.WriteJSON(proto.TResponse, proto.Response{ID: req.ID, OK: true, Result: resBytes}); err != nil {
		return
	}

	s.audit.Record(AuditEntry{Event: "exec", Session: s.id, Detail: api.CleanText(a.Argv[0], 100)})

	// Switch the connection to an internal/link streaming session (spec 10.5).
	clientLink := link.New(&bufferedConn{Conn: conn, r: r}, link.Supervisor, link.Options{
		Opener:               link.BridgeOpener,
		AllowVersionMismatch: true,
	})
	defer func() { _ = clientLink.Close() }()

	streamCtx, streamCancel := context.WithTimeout(context.Background(), 10*time.Second)
	clientSt, err := clientLink.Open(streamCtx, link.KindExec, guest.ExecHeader{ID: execID})
	streamCancel()
	if err != nil {
		return
	}
	defer func() { _ = clientSt.Close() }()

	spliceCtx, cancelSplice := context.WithCancel(context.Background())
	defer cancelSplice()
	go func() {
		select {
		case <-s.doneCh:
			cancelSplice()
			_ = guestSt.Close()
			_ = clientSt.Close()
		case <-spliceCtx.Done():
		}
	}()

	// If the client closes its stream both ways (disconnect or Ctrl+C), close
	// the guest stream immediately so guest init kills the command even when
	// stdin was already closed and the command produces no output.
	go func() {
		if clientSt.WaitPeerGone(spliceCtx) {
			_ = guestSt.Close()
		}
	}()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(clientSt, guestSt)
		_ = clientSt.CloseWrite()
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(guestSt, clientSt)
		_ = guestSt.CloseWrite()
	}()
	wg.Wait()
}
