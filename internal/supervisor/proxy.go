package supervisor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/guest"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

const (
	// maxGuestControlFrame caps non-output frames coming back from the guest
	// holder stream (spec 5.3). TOutput and TSnapshot carry raw terminal bytes
	// up to proto.MaxFrame.
	maxGuestControlFrame = 64 << 10
)

// allowedFromClientAttach lists frame types the supervisor accepts from a
// connected bridge/client on the session socket while attached (spec 6.3).
func allowedFromClientAttach(t proto.Type) bool {
	switch t {
	case proto.TInput, proto.TResize, proto.TDetach, proto.TTrace, proto.TPing:
		return true
	default:
		return false
	}
}

// allowedFromGuest lists frame types the supervisor forwards from a guest
// holder stream to the bridge (spec 6.3). Any other type closes the connection.
func allowedFromGuest(t proto.Type) bool {
	switch t {
	case proto.THello, proto.TError, proto.TPong, proto.TResponse,
		proto.TAttached, proto.TOutput, proto.TSnapshot, proto.TEnded, proto.TEvent:
		return true
	default:
		return false
	}
}

func (s *Supervisor) fullPeer(conn net.Conn) bool {
	return !s.peerCheck || host.PeerOutside(s.id, conn)
}

func (s *Supervisor) serveConn(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReaderSize(conn, 64<<10)
	w := proto.NewWriter(conn)

	f, err := proto.Read(r)
	if err != nil || f.Type != proto.THello {
		return
	}
	var hello proto.Hello
	if json.Unmarshal(f.Payload, &hello) != nil || hello.Check(version.Protocol) != nil {
		_ = w.WriteJSON(proto.TError, proto.Error{Code: "protocol", Message: "incompatible client", Hint: "Update the client."})
		return
	}
	full := s.fullPeer(conn)
	if err := w.WriteJSON(proto.THello, proto.Hello{
		Magic:    proto.Magic,
		Protocol: version.Protocol,
		Version:  version.Version,
		Role:     "supervisor",
		Features: []string{"put", "port.grant", "sftp", "ssh.exec", "arm"},
	}); err != nil {
		return
	}
	if !full {
		s.serveLimited(r, w)
		return
	}

	for {
		f, err := proto.Read(r)
		if err != nil {
			return
		}
		switch f.Type {
		case proto.TPing:
			if err := w.Write(proto.TPong, f.Payload); err != nil {
				return
			}
		case proto.TRequest:
			var req proto.Request
			if json.Unmarshal(f.Payload, &req) != nil {
				return
			}
			if isStreamRequest(req.Op) {
				if s.handleStreamRequest(conn, r, w, req) {
					return
				}
				continue
			}
			if !s.handleRequest(w, req) {
				return
			}
		case proto.TAttach:
			s.handleAttach(conn, r, w, f)
			return
		case proto.TUploadBegin:
			s.handleUpload(conn, r, w, f.Payload)
			return
		case proto.TDetach:
			// Not attached on this connection; ignore harmlessly.
		default:
			s.refuseFrame("client", f.Type)
			return
		}
	}
}

func (s *Supervisor) serveLimited(r *bufio.Reader, w *proto.Writer) {
	refusal := host.ErrInside()
	for {
		f, err := proto.Read(r)
		if err != nil {
			return
		}
		switch f.Type {
		case proto.TPing:
			_ = w.Write(proto.TPong, f.Payload)
		case proto.TRequest:
			var req proto.Request
			if json.Unmarshal(f.Payload, &req) != nil {
				return
			}
			_ = w.WriteJSON(proto.TResponse, proto.Response{ID: req.ID, Error: refusal})
			return
		default:
			_ = w.WriteJSON(proto.TError, refusal)
			return
		}
	}
}

func (s *Supervisor) refuseFrame(dir string, t proto.Type) {
	s.log.Warn("refused disallowed frame", "direction", dir, "type", fmt.Sprintf("0x%02x", byte(t)))
	s.audit.Record(AuditEntry{
		Event:   "frame_refused",
		Session: s.id,
		Detail:  fmt.Sprintf("%s frame 0x%02x", dir, byte(t)),
	})
}

func (s *Supervisor) handleRequest(w *proto.Writer, req proto.Request) bool {
	reply := func(v any, perr *proto.Error) {
		resp := proto.Response{ID: req.ID, OK: perr == nil, Error: perr}
		if v != nil {
			resp.Result, _ = json.Marshal(v)
		}
		_ = w.WriteJSON(proto.TResponse, resp)
	}
	switch req.Op {
	case "info":
		s.mu.Lock()
		m := *s.m
		s.mu.Unlock()
		reply(m, nil)
	case "rename":
		var args struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(req.Args, &args) != nil || strings.TrimSpace(args.Name) == "" {
			reply(nil, &proto.Error{Code: "bad_request", Message: "a name is needed", Hint: "Pass a non-empty name."})
			return true
		}
		name := strings.TrimSpace(args.Name)
		s.mu.Lock()
		s.m.Name = name
		mCopy := *s.m
		s.mu.Unlock()
		_ = s.save(&mCopy, false)
		s.audit.Record(AuditEntry{Event: "rename", Session: s.id, Detail: api.CleanText(name, 100)})
		reply(map[string]string{"name": name}, nil)
	case "sockets":
		reply(host.SocketsState{PeerCheck: s.peerCheck, Version: version.Version}, nil)
	case "hook":
		reply(nil, host.ErrNoHooksHere())
		return false
	case "secrets":
		reply(nil, &proto.Error{Code: "forbidden", Message: "the session has already started", Hint: "Pass secrets when starting or resuming a session."})
		return false
	case "arm":
		s.handleArm(w, req)
	case "kill":
		reply(map[string]bool{"killed": true}, nil)
		go s.requestStop("killed by request", false)
	case "port.grant":
		s.handlePortGrant(w, req)
	case "port.revoke":
		s.handlePortRevoke(w, req)
	case "peek", "send", "ports":
		s.forwardHolderRequest(w, req)
	default:
		reply(nil, &proto.Error{Code: "unknown_op", Message: "unknown request " + req.Op, Hint: "Update the host helper."})
	}
	return true
}

func (s *Supervisor) openHolderStream(ctx context.Context) (*link.Stream, *bufio.Reader, *proto.Writer, error) {
	st, err := s.link.Open(ctx, link.KindHolder, guest.HolderHeader{Session: s.id})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open holder stream: %w", err)
	}
	hw := proto.NewWriter(st)
	hr := bufio.NewReaderSize(st, 64<<10)
	_ = st.SetDeadline(time.Now().Add(5 * time.Second))
	if err := hw.WriteJSON(proto.THello, proto.Hello{
		Magic:    proto.Magic,
		Protocol: version.Protocol,
		Version:  version.Version,
		Role:     "bridge",
	}); err != nil {
		_ = st.Close()
		return nil, nil, nil, err
	}
	f, err := proto.Read(hr)
	if err != nil || f.Type != proto.THello {
		_ = st.Close()
		return nil, nil, nil, errors.New("guest holder did not answer hello")
	}
	_ = st.SetDeadline(time.Time{})
	return st, hr, hw, nil
}

func (s *Supervisor) forwardHolderRequest(w *proto.Writer, req proto.Request) {
	fail := func(code, msg, hint string) {
		_ = w.WriteJSON(proto.TResponse, proto.Response{ID: req.ID, Error: &proto.Error{Code: code, Message: msg, Hint: hint}})
	}
	if req.Op == "send" {
		var args struct {
			Text  string `json:"text"`
			Enter bool   `json:"enter"`
		}
		if json.Unmarshal(req.Args, &args) == nil {
			data := []byte(args.Text)
			if args.Enter {
				data = append(data, '\r')
			}
			s.noteTyping(data)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st, hr, hw, err := s.openHolderStream(ctx)
	if err != nil {
		fail("session_gone", "the session's guest holder did not answer", "Check the session status with ls.")
		return
	}
	defer st.Close()
	_ = st.SetDeadline(time.Now().Add(5 * time.Second))
	if err := hw.WriteJSON(proto.TRequest, req); err != nil {
		fail("session_gone", err.Error(), "Try again.")
		return
	}
	f, err := proto.Read(hr)
	if err != nil {
		fail("session_gone", "the guest holder closed before replying", "Try again.")
		return
	}
	if f.Type != proto.TResponse {
		s.refuseFrame("guest", f.Type)
		fail("protocol", "unexpected frame from guest holder", "Stop and resume the session.")
		return
	}
	if req.Op != "peek" && len(f.Payload) > maxGuestControlFrame {
		s.refuseFrame("guest", f.Type)
		fail("protocol", "guest holder reply was too large", "Stop and resume the session.")
		return
	}
	var resp proto.Response
	if json.Unmarshal(f.Payload, &resp) != nil {
		fail("protocol", "invalid reply from guest holder", "Try again.")
		return
	}
	resp.ID = req.ID
	if !resp.OK {
		resp.Result = nil
		resp.Error = cleanGuestError(resp.Error, "holder", "the guest request failed", "Try again.")
		_ = w.WriteJSON(proto.TResponse, resp)
		return
	}
	switch req.Op {
	case "ports":
		var ports []api.Port
		if json.Unmarshal(resp.Result, &ports) != nil {
			fail("protocol", "invalid port list from guest holder", "Try again.")
			return
		}
		merged := s.mergePorts(cleanPorts(ports))
		resp.Result, _ = json.Marshal(merged)
		_ = w.WriteJSON(proto.TResponse, resp)
	case "send":
		var res struct {
			Sent int `json:"sent"`
		}
		if json.Unmarshal(resp.Result, &res) != nil {
			fail("protocol", "invalid send reply from guest holder", "Try again.")
			return
		}
		resp.Result, _ = json.Marshal(res)
		_ = w.WriteJSON(proto.TResponse, resp)
	default:
		_ = w.WriteJSON(proto.TResponse, resp)
	}
}

// handleAttach pumps frames between conn (the bridge) and a link.KindHolder
// stream. It enforces the frame-type allowlist in both directions and NEVER
// reads or modifies TOutput or TSnapshot payloads (byte-exact).
func (s *Supervisor) handleAttach(conn net.Conn, r *bufio.Reader, w *proto.Writer, attachFrame proto.Frame) {
	var a proto.Attach
	if json.Unmarshal(attachFrame.Payload, &a) == nil {
		s.noteAttach(a)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	st, hr, hw, err := s.openHolderStream(ctx)
	cancel()
	if err != nil {
		_ = w.WriteJSON(proto.TError, proto.Error{Code: "attach_failed", Message: err.Error(), Hint: "List sessions; it may have been stopped."})
		return
	}
	if err := hw.Write(proto.TAttach, attachFrame.Payload); err != nil {
		_ = st.Close()
		return
	}

	var closeOnce sync.Once
	closeBoth := func() {
		closeOnce.Do(func() {
			_ = st.Close()
			_ = conn.Close()
		})
	}
	defer closeBoth()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		defer closeBoth()
		s.pumpGuestToBridge(hr, w)
	}()
	go func() {
		defer wg.Done()
		defer closeBoth()
		s.pumpBridgeToGuest(r, w, hw)
	}()
	wg.Wait()
}

// pumpGuestToBridge forwards allowlisted frames from the guest holder stream to
// the bridge without ever reading or mutating TOutput or TSnapshot payloads.
func (s *Supervisor) pumpGuestToBridge(hr *bufio.Reader, w *proto.Writer) {
	for {
		f, err := proto.Read(hr)
		if err != nil {
			return
		}
		if !allowedFromGuest(f.Type) {
			s.refuseFrame("guest", f.Type)
			return
		}
		if f.Type != proto.TOutput && f.Type != proto.TSnapshot && len(f.Payload) > maxGuestControlFrame {
			s.refuseFrame("guest", f.Type)
			return
		}
		payload := f.Payload
		if f.Type == proto.TAttached {
			payload = markAttachedWalled(payload)
		}
		if err := w.Write(f.Type, payload); err != nil {
			return
		}
	}
}

// pumpBridgeToGuest forwards allowlisted attach frames from the bridge to the
// guest holder stream.
func (s *Supervisor) pumpBridgeToGuest(r *bufio.Reader, w, hw *proto.Writer) {
	for {
		f, err := proto.Read(r)
		if err != nil {
			return
		}
		if !allowedFromClientAttach(f.Type) {
			s.refuseFrame("client", f.Type)
			return
		}
		if !s.forwardAttachFrame(f, w, hw) {
			return
		}
	}
}

func (s *Supervisor) forwardAttachFrame(f proto.Frame, w, hw *proto.Writer) bool {
	switch f.Type {
	case proto.TPing:
		return w.Write(proto.TPong, f.Payload) == nil
	case proto.TInput:
		s.noteTyping(f.Payload)
		return hw.Write(f.Type, f.Payload) == nil
	case proto.TDetach:
		_ = hw.Write(f.Type, f.Payload)
		return false
	default:
		return hw.Write(f.Type, f.Payload) == nil
	}
}
