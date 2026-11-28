package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/pkg/sftp"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client/sshserve"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

// procNetConn adapts a Transport Proc and its buffered stdout reader to
// net.Conn for internal/link over a dedicated bridge connection (spec 10.5).
type procNetConn struct {
	proc      Proc
	br        *bufio.Reader
	closeOnce sync.Once
}

func (p *procNetConn) Read(b []byte) (int, error)  { return p.br.Read(b) }            //nolint:wrapcheck // net.Conn pass-through
func (p *procNetConn) Write(b []byte) (int, error) { return p.proc.Stdin().Write(b) } //nolint:wrapcheck // net.Conn pass-through
func (p *procNetConn) Close() error {
	p.closeOnce.Do(func() {
		_ = p.proc.Stdin().Close()
		p.proc.Kill()
	})
	return nil
}
func (p *procNetConn) LocalAddr() net.Addr              { return pipeAddr("client") }
func (p *procNetConn) RemoteAddr() net.Addr             { return pipeAddr("bridge") }
func (p *procNetConn) SetDeadline(time.Time) error      { return nil }
func (p *procNetConn) SetReadDeadline(time.Time) error  { return nil }
func (p *procNetConn) SetWriteDeadline(time.Time) error { return nil }

type pipeAddr string

func (a pipeAddr) Network() string { return "pipe" }
func (a pipeAddr) String() string  { return string(a) }

type streamRWC struct {
	st     *link.Stream
	closer io.Closer
	once   sync.Once
}

func (s *streamRWC) Read(b []byte) (int, error)  { return s.st.Read(b) }
func (s *streamRWC) Write(b []byte) (int, error) { return s.st.Write(b) }
func (s *streamRWC) CloseWrite() error           { return s.st.CloseWrite() }
func (s *streamRWC) Close() error {
	var err error
	s.once.Do(func() {
		if cerr := s.closer.Close(); cerr != nil {
			err = fmt.Errorf("close bridge stream: %w", cerr)
		}
	})
	return err
}

// OpenSessionSFTP opens a pkg/sftp client to session s on host h. For walled
// sessions it opens an SFTP link stream through the supervisor (spec 10.5,
// 21.1); for unwalled sessions it runs `<helper> sftp <id>`.
func OpenSessionSFTP(ctx context.Context, h Host, s *Session) (*sftp.Client, io.Closer, error) {
	if s == nil {
		return nil, nil, errors.New("missing session")
	}
	t, err := HostTransport(ctx, h, true)
	if err != nil {
		return nil, nil, err
	}
	return openSessionSFTPOn(ctx, t, h, s)
}

func openSessionSFTPOn(ctx context.Context, t Transport, h Host, s *Session) (*sftp.Client, io.Closer, error) {
	if !api.IsWalled(s.Wall) {
		return openSFTPOn(ctx, t, h, s.ID, false)
	}
	st, closer, err := openBridgeStream(ctx, t, h, "sftp", api.SessionStreamArgs{ID: s.ID}, link.KindSFTP)
	if err != nil {
		return nil, nil, err
	}
	sc, err := sftp.NewClientPipe(st, st)
	if err != nil {
		_ = closer.Close()
		return nil, nil, fmt.Errorf("open SFTP in sandbox on %s: %w", h.Name, err)
	}
	allClose := closeFunc(func() error {
		cerr := sc.Close()
		lerr := closer.Close()
		if cerr != nil {
			return fmt.Errorf("close SFTP client: %w", cerr)
		}
		if lerr != nil {
			return fmt.Errorf("close sandbox SFTP stream: %w", lerr)
		}
		return nil
	})
	return sc, allClose, nil
}

func openWalledSFTPStream(ctx context.Context, t Transport, h Host, sessionID string) (io.ReadWriteCloser, error) {
	st, closer, err := openBridgeStream(ctx, t, h, "sftp", api.SessionStreamArgs{ID: sessionID}, link.KindSFTP)
	if err != nil {
		return nil, err
	}
	return &streamRWC{st: st, closer: closer}, nil
}

func startWalledExec(ctx context.Context, t Transport, h Host, sessionID string, args api.SSHExecArgs) (sshserve.ExecStream, error) {
	st, closer, err := openBridgeStream(ctx, t, h, "ssh.exec", api.SSHExecStreamArgs{ID: sessionID, Exec: args}, link.KindExec)
	if err != nil {
		return nil, err
	}
	r := bufio.NewReaderSize(st, 64<<10)
	w := proto.NewWriter(st)
	f, err := proto.Read(r)
	if err != nil || f.Type != proto.TResponse {
		_ = closer.Close()
		return nil, fmt.Errorf("sandbox exec did not answer: %w", err)
	}
	var resp proto.Response
	if err := json.Unmarshal(f.Payload, &resp); err != nil || !resp.OK {
		_ = closer.Close()
		if resp.Error != nil {
			return nil, errors.New(CleanLine(resp.Error.Message))
		}
		return nil, errors.New("sandbox exec refused to start")
	}
	return newRemoteExecStream(r, w, closer.Close), nil
}

// openBridgeStream opens a dedicated bridge connection on t, performs the
// bridge hello and streaming op request, and accepts the supervisor-opened
// internal/link stream of kind wantKind (spec 10.5).
func openBridgeStream(ctx context.Context, t Transport, h Host, op string, args any, wantKind link.Kind) (*link.Stream, io.Closer, error) {
	remote := bridgeCommand(HelperVersion(), 0, helperDigests())
	proc, err := t.Start(context.WithoutCancel(ctx), remote)
	if err != nil {
		return nil, nil, err
	}
	br := bufio.NewReaderSize(proc.Stdout(), 64<<10)
	bw := proto.NewWriter(proc.Stdin())
	abort := func() {
		_ = proc.Stdin().Close()
		proc.Kill()
		_ = proc.Wait()
	}

	if err := bw.WriteJSON(proto.THello, proto.Hello{
		Magic:    proto.Magic,
		Protocol: version.Protocol,
		Version:  version.Version,
		Role:     "client",
	}); err != nil {
		abort()
		return nil, nil, fmt.Errorf("send bridge hello to %s: %w", h.Name, err)
	}
	hf, err := proto.Read(br)
	if err != nil || hf.Type != proto.THello {
		abort()
		return nil, nil, fmt.Errorf("read bridge hello from %s: %w", h.Name, err)
	}
	rawArgs, err := json.Marshal(args)
	if err != nil {
		abort()
		return nil, nil, fmt.Errorf("encode %s request: %w", op, err)
	}
	if err := bw.WriteJSON(proto.TRequest, proto.Request{ID: 1, Op: op, Args: rawArgs}); err != nil {
		abort()
		return nil, nil, fmt.Errorf("send %s request to %s: %w", op, h.Name, err)
	}
	rf, err := proto.Read(br)
	if err != nil || rf.Type != proto.TResponse {
		abort()
		return nil, nil, fmt.Errorf("read %s reply from %s: %w", op, h.Name, err)
	}
	var resp proto.Response
	if err := json.Unmarshal(rf.Payload, &resp); err != nil {
		abort()
		return nil, nil, fmt.Errorf("decode %s reply from %s: %w", op, h.Name, err)
	}
	if !resp.OK {
		abort()
		if resp.Error != nil {
			if resp.Error.Code == "unknown_op" {
				return nil, nil, &UserError{
					Msg:  "the helper on " + h.Name + " is too old for walled " + op,
					Hint: brand.Name + " host upgrade " + ShellQuote(h.Name),
					Code: "wall_helper_old",
				}
			}
			return nil, nil, hostErrorOn(h.Name, resp.Error)
		}
		return nil, nil, fmt.Errorf("%s failed on %s", op, h.Name)
	}

	pconn := &procNetConn{proc: proc, br: br}
	lnk := link.New(pconn, link.Guest, link.Options{
		Opener:               link.BridgeOpener,
		AllowVersionMismatch: true,
		MaxSupervisorStreams: 4,
		MaxGuestStreams:      1,
	})
	st, err := lnk.Accept(ctx)
	if err != nil {
		_ = lnk.Close()
		_ = proc.Wait()
		return nil, nil, fmt.Errorf("open %s stream on %s: %w", op, h.Name, err)
	}
	if st.Kind() != wantKind {
		_ = st.Close()
		_ = lnk.Close()
		_ = proc.Wait()
		return nil, nil, fmt.Errorf("unexpected stream kind %q from %s (want %q)", st.Kind(), h.Name, wantKind)
	}
	closer := closeFunc(func() error {
		_ = st.Close()
		_ = lnk.Close()
		_ = proc.Wait()
		return nil
	})
	return st, closer, nil
}
