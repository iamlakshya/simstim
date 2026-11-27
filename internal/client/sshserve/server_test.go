package sshserve

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

type testBackend struct {
	calls        atomic.Int32
	shell        func(context.Context, Terminal, io.Reader, io.Writer, io.Writer) (TerminalProcess, error)
	execTerminal func(context.Context, string, Terminal, io.Reader, io.Writer, io.Writer) (TerminalProcess, error)
	sftp         func(context.Context) (io.ReadWriteCloser, error)
	port         func(context.Context, uint16) (io.ReadWriteCloser, error)
}

func (b *testBackend) Exec(context.Context, string, io.Reader, io.Writer, io.Writer) (int, error) {
	b.calls.Add(1)
	return 17, nil
}
func (b *testBackend) Shell(ctx context.Context, terminal Terminal, input io.Reader, output, stderr io.Writer) (TerminalProcess, error) {
	b.calls.Add(1)
	if b.shell != nil {
		return b.shell(ctx, terminal, input, output, stderr)
	}
	return nil, errors.New("codec fixture has no native terminal producer")
}
func (b *testBackend) ExecTerminal(ctx context.Context, command string, terminal Terminal, input io.Reader, output, stderr io.Writer) (TerminalProcess, error) {
	b.calls.Add(1)
	if b.execTerminal != nil {
		return b.execTerminal(ctx, command, terminal, input, output, stderr)
	}
	return nil, errors.New("codec fixture has no native terminal producer")
}
func (b *testBackend) SFTP(ctx context.Context) (io.ReadWriteCloser, error) {
	b.calls.Add(1)
	if b.sftp != nil {
		return b.sftp(ctx)
	}
	return nil, errors.New("codec fixture has no guest SFTP producer")
}
func (b *testBackend) Port(ctx context.Context, port uint16) (io.ReadWriteCloser, error) {
	b.calls.Add(1)
	if b.port != nil {
		return b.port(ctx, port)
	}
	return nil, errors.New("the live port is not granted")
}

// A framing actor only. It grants no guest process or native ownership.
type testTerminal struct {
	done      chan struct{}
	once      sync.Once
	resized   atomic.Bool
	signalled atomic.Bool
}

func (p *testTerminal) Resize(_ context.Context, cols, rows uint32) error {
	p.resized.Store(cols == 100 && rows == 40)
	return nil
}
func (p *testTerminal) Signal(_ context.Context, name string) error {
	p.signalled.Store(name == "INT")
	return nil
}
func (p *testTerminal) Wait() (int, error) { <-p.done; return 23, nil }
func (p *testTerminal) Close() error       { p.once.Do(func() { close(p.done) }); return nil }

func TestSSHPTYStartupInstallsProcessBeforeResizeAndSignalReply(t *testing.T) {
	process := &testTerminal{done: make(chan struct{})}
	payload := []byte("terminal\x00\x1b[31m\r\n")
	backend := &testBackend{shell: func(_ context.Context, terminal Terminal, _ io.Reader, output, _ io.Writer) (TerminalProcess, error) {
		if terminal.Term != "xterm-256color" || terminal.Columns != 80 || terminal.Rows != 24 {
			t.Error("valid PTY attributes changed")
		}
		_, err := output.Write(payload)
		return process, err
	}}
	client := stdioSSHClient(t, backend)
	session, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	var output bytes.Buffer
	session.Stdout = &output
	if err = session.RequestPty("xterm-256color", 24, 80, ssh.TerminalModes{}); err != nil {
		t.Fatal(err)
	}
	if err = session.Shell(); err != nil {
		t.Fatal(err)
	}
	if err = session.WindowChange(40, 100); err != nil {
		t.Fatal(err)
	}
	if err = session.Signal(ssh.SIGINT); err != nil {
		t.Fatal(err)
	}
	// WindowChange and Signal have no SSH reply, so an explicit request with
	// a reply observes the ordered processing of both before ending the actor.
	if ok, err := session.SendRequest("unknown-after-start", true, nil); err != nil || ok {
		t.Fatal(err)
	}
	if !process.resized.Load() || !process.signalled.Load() {
		t.Fatal("startup succeeded before its actual process was installed")
	}
	_ = process.Close()
	err = session.Wait()
	var exited *ssh.ExitError
	if !errors.As(err, &exited) || exited.ExitStatus() != 23 || !bytes.Equal(output.Bytes(), payload) {
		t.Fatal("PTY bytes or actual status changed", output.Bytes(), err)
	}
}

func TestSSHExecWithPTYUsesTerminalOperationAndReturnsStatus(t *testing.T) {
	process := &testTerminal{done: make(chan struct{})}
	_ = process.Close()
	backend := &testBackend{execTerminal: func(_ context.Context, command string, _ Terminal, _ io.Reader, _, _ io.Writer) (TerminalProcess, error) {
		if command != "guest-command" {
			t.Error("guest command changed")
		}
		return process, nil
	}}
	client := stdioSSHClient(t, backend)
	session, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if err = session.RequestPty("xterm-256color", 24, 80, ssh.TerminalModes{}); err != nil {
		t.Fatal(err)
	}
	err = session.Run("guest-command")
	var exited *ssh.ExitError
	if !errors.As(err, &exited) || exited.ExitStatus() != 23 || backend.calls.Load() != 1 {
		t.Fatal("PTY exec was refused, duplicated or lost status", err)
	}
}

func TestSSHSFTPAndGrantedLoopbackPortCarryOpaqueBytes(t *testing.T) {
	for _, operation := range []string{"sftp", "port"} {
		t.Run(operation, func(t *testing.T) {
			var pump sync.WaitGroup
			echo := func() (io.ReadWriteCloser, error) {
				left, right := net.Pipe()
				pump.Add(1)
				go func() {
					defer pump.Done()
					defer right.Close()
					buffer := make([]byte, 256)
					for {
						n, err := right.Read(buffer)
						if n > 0 {
							if _, err := right.Write(buffer[:n]); err != nil {
								return
							}
						}
						if err != nil {
							return
						}
					}
				}()
				return left, nil
			}
			backend := &testBackend{sftp: func(context.Context) (io.ReadWriteCloser, error) { return echo() }, port: func(_ context.Context, port uint16) (io.ReadWriteCloser, error) {
				if port != 3000 {
					t.Error("another port reached the producer")
				}
				return echo()
			}}
			client := stdioSSHClient(t, backend)
			var connection io.ReadWriteCloser
			if operation == "sftp" {
				channel, requests, err := client.OpenChannel("session", nil)
				if err != nil {
					t.Fatal(err)
				}
				go ssh.DiscardRequests(requests)
				if ok, err := channel.SendRequest("subsystem", true, ssh.Marshal(struct{ Name string }{"sftp"})); err != nil || !ok {
					t.Fatal("the exact SFTP request was refused", err)
				}
				connection = channel
			} else {
				var err error
				connection, err = client.Dial("tcp", "127.0.0.1:3000")
				if err != nil {
					t.Fatal(err)
				}
			}
			payload := []byte{0, 1, 255, '\x1b', '[', '3', '1', 'm', '\r', '\n'}
			written := make(chan error, 1)
			go func() { _, err := connection.Write(payload); written <- err }()
			got := make([]byte, len(payload))
			if _, err := io.ReadFull(connection, got); err != nil {
				t.Fatal(err)
			}
			if err := <-written; err != nil || !bytes.Equal(got, payload) {
				t.Fatal("opaque stream bytes changed", got, err)
			}
			_ = connection.Close()
			joined := make(chan struct{})
			go func() { pump.Wait(); close(joined) }()
			select {
			case <-joined:
			case <-time.After(time.Second):
				t.Fatal("closing the SSH operation did not join its actual stream pump")
			}
		})
	}
}

func TestStalledPortAdmissionDoesNotBlockAnotherSFTPChannel(t *testing.T) {
	portEntered := make(chan struct{})
	releasePort := make(chan struct{})
	backend := &testBackend{port: func(ctx context.Context, _ uint16) (io.ReadWriteCloser, error) {
		close(portEntered)
		select {
		case <-releasePort:
			return nil, errors.New("port admission deliberately refused after its owned wait")
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	backend.sftp = func(context.Context) (io.ReadWriteCloser, error) {
		left, right := net.Pipe()
		_ = right.Close()
		return left, nil
	}
	client := stdioSSHClient(t, backend)
	portDone := make(chan error, 1)
	go func() {
		connection, err := client.Dial("tcp", "127.0.0.1:3000")
		if connection != nil {
			_ = connection.Close()
		}
		portDone <- err
	}()
	select {
	case <-portEntered:
	case <-time.After(time.Second):
		t.Fatal("port request did not reach its owned backend wait")
	}
	sftpReady := make(chan error, 1)
	go func() {
		channel, requests, err := client.OpenChannel("session", nil)
		if err != nil {
			sftpReady <- err
			return
		}
		defer channel.Close()
		go ssh.DiscardRequests(requests)
		ok, err := channel.SendRequest("subsystem", true, ssh.Marshal(struct{ Name string }{"sftp"}))
		if err == nil && !ok {
			err = errors.New("unrelated SFTP startup was refused")
		}
		sftpReady <- err
	}()
	select {
	case err := <-sftpReady:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("a stalled port-open blocked an unrelated session/SFTP channel")
	}
	close(releasePort)
	if err := <-portDone; err == nil {
		t.Fatal("the deliberately refused port was accepted")
	}
}
func (*testBackend) Close(context.Context) error { return nil }

// This tests actual SSH framing over owned OS pipes. Its operation recorder
// grants no native permission and is never counted as sandbox/runtime proof.
func stdioSSHClient(t *testing.T, backend Backend) *ssh.Client {
	t.Helper()
	serverIn, clientOut, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	clientIn, serverOut, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	finished := make(chan error, 1)
	go func() { finished <- Serve(ctx, serverIn, serverOut, signer, backend) }()
	transport := &stdioConnection{input: clientIn, output: clientOut}
	config := &ssh.ClientConfig{User: "agent", HostKeyCallback: ssh.FixedHostKey(signer.PublicKey())}
	connection, channels, requests, err := ssh.NewClientConn(transport, "stdio", config)
	if err != nil {
		cancel()
		_ = transport.Close()
		t.Fatal(err)
	}
	client := ssh.NewClient(connection, channels, requests)
	t.Cleanup(func() { cancel(); _ = client.Close(); _ = transport.Close(); <-finished })
	return client
}

func TestSSHRefusesEveryForwardingGlobalAndUnknownChannel(t *testing.T) {
	backend := &testBackend{}
	client := stdioSSHClient(t, backend)
	for _, request := range []string{"tcpip-forward", "cancel-tcpip-forward", "streamlocal-forward@openssh.com", "unknown-global"} {
		if ok, _, err := client.SendRequest(request, true, nil); err != nil || ok {
			t.Fatal("forbidden global request was accepted or hung", request, err)
		}
	}
	for _, kind := range []string{"x11", "auth-agent@openssh.com", "direct-streamlocal@openssh.com", "unknown-channel"} {
		if channel, _, err := client.OpenChannel(kind, nil); err == nil {
			_ = channel.Close()
			t.Fatal("forbidden channel was accepted", kind)
		}
	}
	if backend.calls.Load() != 0 {
		t.Fatal("a refused SSH declaration reached an operation producer")
	}
}

func TestSSHSessionRefusesEnvironmentForwardingAndUnsupportedSubsystem(t *testing.T) {
	backend := &testBackend{}
	client := stdioSSHClient(t, backend)
	channel, requests, err := client.OpenChannel("session", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer channel.Close()
	go ssh.DiscardRequests(requests)
	for _, request := range []struct {
		name    string
		payload []byte
	}{{"env", ssh.Marshal(struct{ Key, Value string }{"KEY", "value"})}, {"x11-req", nil}, {"auth-agent-req@openssh.com", nil}, {"unknown-session", nil}, {"subsystem", ssh.Marshal(struct{ Name string }{"other"})}, {"window-change", ssh.Marshal(struct{ Columns, Rows, Width, Height uint32 }{80, 24, 0, 0})}, {"signal", ssh.Marshal(struct{ Name string }{"TERM"})}} {
		if ok, err := channel.SendRequest(request.name, true, request.payload); err != nil || ok {
			t.Fatal("forbidden or out-of-order session request was accepted or hung", request.name, err)
		}
	}
	if backend.calls.Load() != 0 {
		t.Fatal("rejected session metadata reached an operation producer")
	}
}

func TestSSHExecReturnsActualStatusAndRefusesSecondStartup(t *testing.T) {
	backend := &testBackend{}
	client := stdioSSHClient(t, backend)
	session, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	err = session.Run("printf guest-command")
	var exited *ssh.ExitError
	if !errors.As(err, &exited) || exited.ExitStatus() != 17 || backend.calls.Load() != 1 {
		t.Fatal("SSH exec lost its actual operation status", err)
	}
}

func TestSSHDirectTCPNeverCallsBackendForNonLoopback(t *testing.T) {
	backend := &testBackend{}
	client := stdioSSHClient(t, backend)
	for _, host := range []string{"192.0.2.1", "service.example", "/run/socket"} {
		channel, _, err := client.OpenChannel("direct-tcpip", ssh.Marshal(directTCP{Host: host, Port: 8080}))
		if err == nil {
			_ = channel.Close()
			t.Fatal("non-guest forwarding was accepted")
		}
	}
	if backend.calls.Load() != 0 {
		t.Fatal("a remote address selected an actual dial before its refusal")
	}
}
