package supervisor

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/guest"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

const streamOpenTimeout = 5 * time.Second

func isStreamRequest(op string) bool {
	return op == "sftp" || op == "ssh.exec" || op == "pull.bundle"
}

// bufferedConn wraps a net.Conn with a bufio.Reader so bytes already read
// during the TRequest handshake are not lost when switching to link framing.
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (b *bufferedConn) Read(p []byte) (int, error) { return b.r.Read(p) } //nolint:wrapcheck // net.Conn pass-through

func (b *bufferedConn) CloseWrite() error {
	if cw, ok := b.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return nil
}

// handleStreamRequest handles streaming ops ("sftp", "ssh.exec") on the
// session socket per isolation spec 10.5. After sending an OK TResponse, the
// connection carries internal/link frames with link.BridgeOpener and
// AllowVersionMismatch, splicing the odd-ID client stream with the matching
// guest stream.
func (s *Supervisor) handleStreamRequest(conn net.Conn, r *bufio.Reader, w *proto.Writer, req proto.Request) bool {
	if req.Op == "pull.bundle" {
		return s.handlePullBundle(conn, w, req)
	}
	fail := func(code, msg, hint string) bool {
		_ = w.WriteJSON(proto.TResponse, proto.Response{
			ID:    req.ID,
			Error: &proto.Error{Code: code, Message: msg, Hint: hint},
		})
		return false
	}
	if s.link == nil {
		return fail("sandbox_stopped", "the sandbox link is not running", "Resume the session and try again.")
	}

	var kind link.Kind
	var guestHdr, cliHdr any
	var auditEvent, auditDetail string

	switch req.Op {
	case "sftp":
		kind = link.KindSFTP
		guestHdr = guest.SFTPHeader{Session: s.id}
		auditEvent = "sftp_open"
	case "ssh.exec":
		var a api.SSHExecStreamArgs
		if err := json.Unmarshal(req.Args, &a); err != nil {
			return fail("bad_request", "the ssh.exec request is not valid JSON", "Update the client.")
		}
		execID := randomStreamID()
		kind = link.KindExec
		guestHdr = guest.ExecHeader{ID: execID, Session: s.id, Mode: "ssh", SSH: &a.Exec}
		cliHdr = guest.ExecHeader{ID: execID}
		auditEvent = "exec_open"
		if a.Exec.Command != "" {
			auditDetail = api.CleanText(a.Exec.Command, 120)
		} else {
			auditDetail = "shell"
		}
	default:
		return fail("unknown_op", "unknown stream request "+req.Op, "Update the host helper.")
	}

	ctx, cancel := context.WithTimeout(context.Background(), streamOpenTimeout)
	guestSt, err := s.link.Open(ctx, kind, guestHdr)
	cancel()
	if err != nil {
		return fail("session_gone", "could not open guest "+string(kind)+" stream: "+err.Error(), "Check the session status with ls.")
	}
	if err := w.WriteJSON(proto.TResponse, proto.Response{ID: req.ID, OK: true}); err != nil {
		_ = guestSt.Close()
		return true
	}

	cliLink := link.New(&bufferedConn{Conn: conn, r: r}, link.Supervisor, link.Options{
		Opener:               link.BridgeOpener,
		AllowVersionMismatch: true,
		MaxSupervisorStreams: 4,
		MaxGuestStreams:      1,
	})
	octx, ocancel := context.WithTimeout(context.Background(), streamOpenTimeout)
	cliSt, err := cliLink.Open(octx, kind, cliHdr)
	ocancel()
	if err != nil {
		s.log.Warn("open client bridge stream failed", "kind", kind, "err", err)
		_ = guestSt.Close()
		_ = cliLink.Close()
		return true
	}

	s.audit.Record(AuditEntry{Event: auditEvent, Session: s.id, Detail: auditDetail})
	spliceBridgeStreams(cliSt, guestSt)
	_ = guestSt.Close()
	_ = cliSt.Close()
	_ = cliLink.Close()
	return true
}

// spliceBridgeStreams relays bytes between cliSt (client-facing link) and
// guestSt (guest-facing link). A write half-close propagates via CloseWrite; a
// full close from the client immediately closes guestSt so the guest tears
// down SFTP or kills an exec command (spec 10.5).
func spliceBridgeStreams(cliSt, guestSt *link.Stream) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		copyOneWay(guestSt, cliSt)
		if cliSt.PeerGone() {
			_ = guestSt.Close()
		}
	}()
	go func() {
		defer wg.Done()
		copyOneWay(cliSt, guestSt)
		if guestSt.PeerGone() {
			_ = cliSt.Close()
		}
	}()
	wg.Wait()
}

func randomStreamID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
