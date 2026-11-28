package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"

	"github.com/pkg/sftp"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client/sshserve"
	"github.com/iamlakshya/simstim/internal/proto"
)

// procRWC wraps a Transport Proc's stdin/stdout as an io.ReadWriteCloser with
// CloseWrite support for SFTP and port splicing.
type procRWC struct {
	proc Proc
	r    io.Reader
}

func (p *procRWC) Read(b []byte) (int, error)  { return p.r.Read(b) }             //nolint:wrapcheck // io.ReadWriteCloser pass-through
func (p *procRWC) Write(b []byte) (int, error) { return p.proc.Stdin().Write(b) } //nolint:wrapcheck // io.ReadWriteCloser pass-through
func (p *procRWC) CloseWrite() error           { return p.proc.Stdin().Close() }  //nolint:wrapcheck // io.ReadWriteCloser pass-through
func (p *procRWC) Close() error {
	_ = p.proc.Stdin().Close()
	p.proc.Kill()
	_ = p.proc.Wait()
	return nil
}

// OpenSFTP opens a pkg/sftp client to session id on host h through the host's
// Transport (`<helper> sftp <id>`). The returned closer shuts down the remote
// process as well as the SFTP client.
func OpenSFTP(ctx context.Context, h Host, sessionID string) (*sftp.Client, io.Closer, error) {
	t, err := HostTransport(ctx, h, true)
	if err != nil {
		return nil, nil, err
	}
	return openSFTPOn(ctx, t, h, sessionID)
}

func openSFTPOn(ctx context.Context, t Transport, h Host, sessionID string) (*sftp.Client, io.Closer, error) {
	remote := RemoteHelper(HelperVersion()) + " sftp " + shellWord(sessionID)
	proc, err := t.Start(context.WithoutCancel(ctx), remote)
	if err != nil {
		return nil, nil, err
	}
	sc, err := sftp.NewClientPipe(proc.Stdout(), proc.Stdin())
	if err != nil {
		proc.Kill()
		_ = proc.Wait()
		msg := tailStderr(proc.Stderr())
		if msg != "" {
			return nil, nil, &UserError{
				Msg:  fmt.Sprintf("could not open SFTP on %s: %s", h.Name, CleanLine(msg)),
				Hint: brand.Name + " host add " + h.Name + " (updates the helper)",
				Code: "sftp_failed",
			}
		}
		return nil, nil, fmt.Errorf("open SFTP on %s: %w", h.Name, err)
	}
	closer := closeFunc(func() error {
		cerr := sc.Close()
		_ = proc.Stdin().Close()
		_ = proc.Wait()
		if cerr != nil {
			return fmt.Errorf("close SFTP client: %w", cerr)
		}
		return nil
	})
	return sc, closer, nil
}

type closeFunc func() error

func (f closeFunc) Close() error { return f() }

// SessionSSHBackend connects sshserve (ssh-proxy) to one session on host h
// through the host's Transport. When Walled is true, SFTP and exec run inside
// the sandbox through the supervisor's link streams (spec 10.5, 17.7).
type SessionSSHBackend struct {
	Host      Host
	SessionID string
	Transport Transport
	Walled    bool
}

// OpenSFTP opens SFTP inside the sandbox (when Walled) or starts
// `<helper> sftp <id>` over the host transport.
func (b *SessionSSHBackend) OpenSFTP(ctx context.Context) (io.ReadWriteCloser, error) {
	if b.Walled {
		return openWalledSFTPStream(ctx, b.Transport, b.Host, b.SessionID)
	}
	remote := RemoteHelper(HelperVersion()) + " sftp " + shellWord(b.SessionID)
	proc, err := b.Transport.Start(context.WithoutCancel(ctx), remote)
	if err != nil {
		return nil, err
	}
	return &procRWC{proc: proc, r: proc.Stdout()}, nil
}

// DialPort starts `<helper> dial-port <id> <host> <port>` over the host
// transport and waits for the one-line JSON status reply before returning the
// raw stream.
func (b *SessionSSHBackend) DialPort(ctx context.Context, host string, port uint32) (io.ReadWriteCloser, error) {
	remote := RemoteHelper(HelperVersion()) + " dial-port " + shellWord(b.SessionID) + " " + shellWord(host) + " " + strconv.FormatUint(uint64(port), 10)
	proc, err := b.Transport.Start(context.WithoutCancel(ctx), remote)
	if err != nil {
		return nil, err
	}
	br := bufio.NewReaderSize(proc.Stdout(), 32<<10)
	line, err := br.ReadBytes('\n')
	if err != nil {
		proc.Kill()
		_ = proc.Wait()
		return nil, fmt.Errorf("dial-port failed: %w", err)
	}
	var rep api.DialPortReply
	if err := json.Unmarshal(line, &rep); err != nil {
		proc.Kill()
		_ = proc.Wait()
		return nil, fmt.Errorf("read dial-port reply: %w", err)
	}
	if !rep.OK {
		proc.Kill()
		_ = proc.Wait()
		return nil, errors.New(CleanLine(rep.Error))
	}
	return &procRWC{proc: proc, r: br}, nil
}

// StartExec starts exec inside the sandbox through the supervisor (when
// Walled) or `<helper> ssh-exec <id>` over the host transport, returning an
// ExecStream that separates stdout, stderr, stdin EOF, resizes, signals and
// exit status.
func (b *SessionSSHBackend) StartExec(ctx context.Context, args api.SSHExecArgs) (sshserve.ExecStream, error) {
	if b.Walled {
		return startWalledExec(ctx, b.Transport, b.Host, b.SessionID, args)
	}
	remote := RemoteHelper(HelperVersion()) + " ssh-exec " + shellWord(b.SessionID)
	proc, err := b.Transport.Start(context.WithoutCancel(ctx), remote)
	if err != nil {
		return nil, err
	}
	w := proto.NewWriter(proc.Stdin())
	r := bufio.NewReaderSize(proc.Stdout(), 64<<10)
	rawArgs, _ := json.Marshal(args)
	if err := w.WriteJSON(proto.TRequest, proto.Request{ID: 1, Op: "exec", Args: rawArgs}); err != nil {
		proc.Kill()
		_ = proc.Wait()
		return nil, err
	}
	f, err := proto.Read(r)
	if err != nil || f.Type != proto.TResponse {
		proc.Kill()
		_ = proc.Wait()
		return nil, fmt.Errorf("ssh-exec did not answer: %w", err)
	}
	var resp proto.Response
	if err := json.Unmarshal(f.Payload, &resp); err != nil || !resp.OK {
		proc.Kill()
		_ = proc.Wait()
		if resp.Error != nil {
			return nil, errors.New(CleanLine(resp.Error.Message))
		}
		return nil, errors.New("ssh-exec refused to start")
	}

	return newRemoteExecStream(r, w, func() error {
		_ = proc.Stdin().Close()
		proc.Kill()
		_ = proc.Wait()
		return nil
	}), nil
}

func newRemoteExecStream(r *bufio.Reader, w *proto.Writer, closeFn func() error) *remoteExecStream {
	outR, outW := io.Pipe()
	errR, errW := io.Pipe()
	es := &remoteExecStream{
		closeFn: closeFn,
		w:       w,
		outR:    outR,
		errR:    errR,
		done:    make(chan struct{}),
	}
	es.stdin = &protoStdinWriter{w: w}
	go es.readLoop(r, outW, errW)
	return es
}

type protoStdinWriter struct {
	mu     sync.Mutex
	w      *proto.Writer
	closed bool
}

func (p *protoStdinWriter) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return 0, io.ErrClosedPipe
	}
	for off := 0; off < len(b); off += 32 << 10 {
		end := min(off+32<<10, len(b))
		if err := p.w.Write(proto.TInput, b[off:end]); err != nil {
			return off, err
		}
	}
	return len(b), nil
}

func (p *protoStdinWriter) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	return p.w.Write(proto.TInput, nil)
}

type remoteExecStream struct {
	closeFn func() error
	w       *proto.Writer
	stdin   *protoStdinWriter
	outR    *io.PipeReader
	errR    *io.PipeReader

	done     chan struct{}
	exitCode int
	closeMu  sync.Mutex
	closed   bool
}

func (e *remoteExecStream) readLoop(r *bufio.Reader, outW, errW *io.PipeWriter) {
	defer close(e.done)
	defer outW.Close()
	defer errW.Close()
	e.exitCode = 1
	for {
		f, err := proto.Read(r)
		if err != nil {
			return
		}
		switch f.Type {
		case proto.TOutput:
			if len(f.Payload) < 2 {
				continue
			}
			switch f.Payload[0] {
			case 1:
				_, _ = outW.Write(f.Payload[1:])
			case 2:
				_, _ = errW.Write(f.Payload[1:])
			}
		case proto.TEnded:
			var ended proto.Ended
			if json.Unmarshal(f.Payload, &ended) == nil {
				e.exitCode = ended.ExitCode
			}
			return
		default:
		}
	}
}

func (e *remoteExecStream) Stdin() io.WriteCloser { return e.stdin }
func (e *remoteExecStream) Stdout() io.Reader     { return e.outR }
func (e *remoteExecStream) Stderr() io.Reader     { return e.errR }

func (e *remoteExecStream) Resize(cols, rows int) error {
	return e.w.WriteJSON(proto.TResize, proto.Resize{Cols: cols, Rows: rows})
}

func (e *remoteExecStream) Signal(name string) error {
	return e.w.WriteJSON(proto.TEvent, api.SSHSignalEvent{Signal: name})
}

func (e *remoteExecStream) Wait() (int, error) {
	<-e.done
	return e.exitCode, nil
}

func (e *remoteExecStream) Close() error {
	e.closeMu.Lock()
	if e.closed {
		e.closeMu.Unlock()
		return nil
	}
	e.closed = true
	e.closeMu.Unlock()
	if e.closeFn != nil {
		return e.closeFn()
	}
	return nil
}
