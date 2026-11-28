package sshserve

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/iamlakshya/simstim/internal/api"
)

// MaxChannelsPerSecond caps SSH channel opens on one proxy connection (spec 17.7).
const MaxChannelsPerSecond = 20

// ExecStream is one running exec command or ephemeral PTY shell on the session.
type ExecStream interface {
	Stdin() io.WriteCloser
	Stdout() io.Reader
	Stderr() io.Reader
	Resize(cols, rows int) error
	Signal(name string) error
	Wait() (int, error)
	Close() error
}

// Backend connects an accepted SSH channel in ssh-proxy to the remote session.
type Backend interface {
	OpenSFTP(ctx context.Context) (io.ReadWriteCloser, error)
	StartExec(ctx context.Context, args api.SSHExecArgs) (ExecStream, error)
	DialPort(ctx context.Context, host string, port uint32) (io.ReadWriteCloser, error)
}

// channelLimiter enforces a token-bucket rate limit of MaxChannelsPerSecond.
type channelLimiter struct {
	mu     sync.Mutex
	tokens float64
	last   time.Time
	rate   float64
}

func newChannelLimiter(rate int) *channelLimiter {
	return &channelLimiter{tokens: float64(rate), last: time.Now(), rate: float64(rate)}
}

func (l *channelLimiter) allow() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	elapsed := now.Sub(l.last).Seconds()
	l.last = now
	l.tokens += elapsed * l.rate
	if l.tokens > l.rate {
		l.tokens = l.rate
	}
	if l.tokens < 1 {
		return false
	}
	l.tokens--
	return true
}

// Serve runs the client-side SSH server over conn using hostKey and backend.
// Every disallowed global request, channel type or channel request receives an
// explicit refusal reply so no SSH client hangs (spec 17.7).
func Serve(ctx context.Context, conn net.Conn, hostKey ssh.Signer, backend Backend) error {
	cfg := &ssh.ServerConfig{
		// Outer SSH or provider transport already authenticated the user.
		NoClientAuth: true,
	}
	cfg.AddHostKey(hostKey)

	sconn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return fmt.Errorf("SSH handshake failed: %w", err)
	}
	defer sconn.Close()

	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		<-cctx.Done()
		_ = sconn.Close()
	}()

	// Refuse every global request (tcpip-forward, streamlocal-forward, etc.)
	// with a reply so `ssh -R` fails immediately instead of hanging.
	go func() {
		for req := range reqs {
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
		}
	}()

	limiter := newChannelLimiter(MaxChannelsPerSecond)
	var wg sync.WaitGroup
	for newChan := range chans {
		if !limiter.allow() {
			_ = newChan.Reject(ssh.ResourceShortage, "too many channels opened per second")
			continue
		}
		switch newChan.ChannelType() {
		case "session":
			ch, chReqs, err := newChan.Accept()
			if err != nil {
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				handleSessionChannel(cctx, ch, chReqs, backend)
			}()
		case "direct-tcpip":
			handleDirectTCPIP(cctx, newChan, backend, &wg)
		default:
			// Refuses direct-streamlocal@openssh.com and any unknown channel.
			_ = newChan.Reject(ssh.UnknownChannelType, "channel type not allowed")
		}
	}
	wg.Wait()
	return nil
}

func handleDirectTCPIP(ctx context.Context, newChan ssh.NewChannel, backend Backend, wg *sync.WaitGroup) {
	var payload struct {
		DestAddr   string
		DestPort   uint32
		OriginAddr string
		OriginPort uint32
	}
	if err := ssh.Unmarshal(newChan.ExtraData(), &payload); err != nil {
		_ = newChan.Reject(ssh.ConnectionFailed, "bad direct-tcpip payload")
		return
	}
	if !isLoopback(payload.DestAddr) {
		_ = newChan.Reject(ssh.Prohibited, fmt.Sprintf("forwarding to %s is refused; only loopback (127.0.0.1, ::1, localhost) is allowed", payload.DestAddr))
		return
	}
	if payload.DestPort == 0 || payload.DestPort > 65535 {
		_ = newChan.Reject(ssh.Prohibited, "invalid port")
		return
	}
	remote, err := backend.DialPort(ctx, payload.DestAddr, payload.DestPort)
	if err != nil {
		_ = newChan.Reject(ssh.ConnectionFailed, err.Error())
		return
	}
	ch, chReqs, err := newChan.Accept()
	if err != nil {
		_ = remote.Close()
		return
	}
	go ssh.DiscardRequests(chReqs)
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer ch.Close()
		defer remote.Close()
		spliceStreams(ch, remote)
	}()
}

func isLoopback(addr string) bool {
	return addr == "127.0.0.1" || addr == "::1" || addr == "localhost"
}

type sessionChannel struct {
	ctx     context.Context
	ch      ssh.Channel
	backend Backend
	doneWG  *sync.WaitGroup

	hasPTY  bool
	term    string
	cols    int
	rows    int
	started bool
	mu      sync.Mutex
	proc    ExecStream
}

func handleSessionChannel(ctx context.Context, ch ssh.Channel, reqs <-chan *ssh.Request, backend Backend) {
	defer ch.Close()
	var doneWG sync.WaitGroup
	defer doneWG.Wait()
	sc := &sessionChannel{ctx: ctx, ch: ch, backend: backend, doneWG: &doneWG}
	for req := range reqs {
		if !sc.handleRequest(req) {
			return
		}
	}
}

func (s *sessionChannel) handleRequest(req *ssh.Request) bool {
	switch req.Type {
	case "pty-req":
		s.handlePTY(req)
	case "window-change":
		s.handleWindowChange(req)
	case "signal":
		s.handleSignal(req)
	case "shell":
		return s.handleStartExec(req, "")
	case "exec":
		return s.handleExec(req)
	case "subsystem":
		return s.handleSubsystem(req)
	default:
		// Refuses x11-req, auth-agent-req@openssh.com, env (SendEnv), and
		// any unknown request with an explicit false reply.
		reply(req, false)
	}
	return true
}

func (s *sessionChannel) handlePTY(req *ssh.Request) {
	if s.started {
		reply(req, false)
		return
	}
	var p struct {
		Term   string
		Cols   uint32
		Rows   uint32
		Width  uint32
		Height uint32
		Modes  string
	}
	if err := ssh.Unmarshal(req.Payload, &p); err != nil {
		reply(req, false)
		return
	}
	s.hasPTY, s.term, s.cols, s.rows = true, p.Term, int(p.Cols), int(p.Rows)
	reply(req, true)
}

func (s *sessionChannel) handleWindowChange(req *ssh.Request) {
	var w struct {
		Cols   uint32
		Rows   uint32
		Width  uint32
		Height uint32
	}
	if err := ssh.Unmarshal(req.Payload, &w); err != nil {
		reply(req, false)
		return
	}
	s.cols, s.rows = int(w.Cols), int(w.Rows)
	if p := s.currentProc(); p != nil {
		_ = p.Resize(s.cols, s.rows)
	}
	reply(req, true)
}

func (s *sessionChannel) handleSignal(req *ssh.Request) {
	var sig struct{ Name string }
	if err := ssh.Unmarshal(req.Payload, &sig); err != nil {
		reply(req, false)
		return
	}
	if p := s.currentProc(); p != nil {
		_ = p.Signal(sig.Name)
	}
	reply(req, true)
}

func (s *sessionChannel) handleExec(req *ssh.Request) bool {
	if s.started {
		reply(req, false)
		return true
	}
	var e struct{ Command string }
	if err := ssh.Unmarshal(req.Payload, &e); err != nil {
		reply(req, false)
		return true
	}
	return s.handleStartExec(req, e.Command)
}

func (s *sessionChannel) handleStartExec(req *ssh.Request, command string) bool {
	if s.started {
		reply(req, false)
		return true
	}
	es, err := s.backend.StartExec(s.ctx, api.SSHExecArgs{
		Command: command, PTY: s.hasPTY, Term: s.term, Cols: s.cols, Rows: s.rows,
	})
	if err != nil {
		reply(req, false)
		return false
	}
	s.started = true
	s.mu.Lock()
	s.proc = es
	s.mu.Unlock()
	reply(req, true)
	s.doneWG.Add(1)
	go func() {
		defer s.doneWG.Done()
		pumpExec(s.ch, es)
	}()
	return true
}

func (s *sessionChannel) handleSubsystem(req *ssh.Request) bool {
	if s.started {
		reply(req, false)
		return true
	}
	var sub struct{ Name string }
	if err := ssh.Unmarshal(req.Payload, &sub); err != nil || sub.Name != "sftp" {
		reply(req, false)
		return true
	}
	sftpStream, err := s.backend.OpenSFTP(s.ctx)
	if err != nil {
		reply(req, false)
		return false
	}
	s.started = true
	reply(req, true)
	s.doneWG.Add(1)
	go func() {
		defer s.doneWG.Done()
		defer sftpStream.Close()
		defer s.ch.Close()
		spliceStreams(s.ch, sftpStream)
		var status [4]byte
		_, _ = s.ch.SendRequest("exit-status", false, status[:])
	}()
	return true
}

func (s *sessionChannel) currentProc() ExecStream {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.proc
}

func reply(req *ssh.Request, ok bool) {
	if req.WantReply {
		_ = req.Reply(ok, nil)
	}
}

func pumpExec(ch ssh.Channel, es ExecStream) {
	defer es.Close()
	defer ch.Close()

	go func() {
		_, _ = io.Copy(es.Stdin(), ch)
		_ = es.Stdin().Close()
	}()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(ch, es.Stdout())
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(ch.Stderr(), es.Stderr())
	}()
	wg.Wait()

	code, _ := es.Wait()
	var status [4]byte
	binary.BigEndian.PutUint32(status[:], uint32(code)) //nolint:gosec // SSH exit status is uint32
	_, _ = ch.SendRequest("exit-status", false, status[:])
}

func spliceStreams(ch ssh.Channel, remote io.ReadWriteCloser) {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(remote, ch)
		if cw, ok := remote.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
	}()
	_, _ = io.Copy(ch, remote)
	_ = ch.CloseWrite()
	_ = remote.Close()
	wg.Wait()
}
