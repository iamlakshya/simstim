package host

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

// isStreamingOp reports whether op switches its dedicated bridge connection
// into raw link-frame relay mode after its TResponse (isolation spec 10.5).
func isStreamingOp(op string) bool {
	switch op {
	case "sftp", "ssh.exec":
		return true
	default:
		return false
	}
}

// serveStream handles a streaming op synchronously on the bridge connection.
// After forwarding the supervisor's OK TResponse, it passes all subsequent
// link frames between client and supervisor unchanged (spec 10.5) and returns
// true so RunBridge exits when the stream ends.
func (b *bridge) serveStream(req proto.Request, r *bufio.Reader, out io.Writer) bool {
	var target struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(req.Args, &target); err != nil || !validID(target.ID) {
		b.reply(req.ID, nil, UserError("not_found", "no session "+target.ID, "List sessions with ls."))
		return false
	}
	m, err := LoadManifest(target.ID)
	if err != nil {
		b.reply(req.ID, nil, UserError("not_found", "no session "+target.ID, "List sessions with ls."))
		return false
	}
	if !isWalled(m) {
		b.reply(req.ID, nil, UserError("bad_request", "streaming "+req.Op+" applies only to walled sessions", "Connect to an unwalled session directly."))
		return false
	}

	d := net.Dialer{Timeout: 3 * time.Second}
	supConn, err := d.DialContext(context.Background(), "unix", paths.Socket(target.ID))
	if err != nil {
		b.reply(req.ID, nil, UserError("sandbox_stopped",
			fmt.Sprintf("session %s is not running", m.Name),
			"Start or resume the session first."))
		return false
	}
	supR, resp, err := handshakeStreamSupervisor(supConn, req)
	if err != nil {
		_ = supConn.Close()
		b.reply(req.ID, nil, err)
		return false
	}
	_ = b.w.WriteJSON(proto.TResponse, resp)
	if !resp.OK {
		_ = supConn.Close()
		return false
	}

	// Pass link frames between client and supervisor unchanged (spec 10.5).
	go func() {
		_, _ = io.Copy(supConn, r)
		if cw, ok := supConn.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
	}()
	_, _ = io.Copy(out, supR)
	_ = supConn.Close()
	return true
}

func handshakeStreamSupervisor(supConn net.Conn, req proto.Request) (*bufio.Reader, proto.Response, error) {
	_ = supConn.SetDeadline(time.Now().Add(10 * time.Second))
	supW := proto.NewWriter(supConn)
	supR := bufio.NewReaderSize(supConn, 64<<10)
	if err := supW.WriteJSON(proto.THello, proto.Hello{
		Magic:    proto.Magic,
		Protocol: version.Protocol,
		Version:  version.Version,
		Role:     "bridge",
	}); err != nil {
		return nil, proto.Response{}, fmt.Errorf("write hello to supervisor: %w", err)
	}
	hf, err := proto.Read(supR)
	if err != nil || hf.Type != proto.THello {
		return nil, proto.Response{}, UserError("session_gone", "the session's supervisor did not answer", "Check the session status with ls.")
	}
	if err := supW.WriteJSON(proto.TRequest, req); err != nil {
		return nil, proto.Response{}, fmt.Errorf("forward %s to supervisor: %w", req.Op, err)
	}
	rf, err := proto.Read(supR)
	if err != nil || rf.Type != proto.TResponse {
		return nil, proto.Response{}, UserError("session_gone", "the session's supervisor closed before replying", "Check the session status with ls.")
	}
	var resp proto.Response
	if err := json.Unmarshal(rf.Payload, &resp); err != nil {
		return nil, proto.Response{}, fmt.Errorf("decode supervisor %s reply: %w", req.Op, err)
	}
	resp.ID = req.ID
	_ = supConn.SetDeadline(time.Time{})
	return supR, resp, nil
}
