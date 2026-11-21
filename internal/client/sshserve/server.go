package sshserve

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/iamlakshya/simstim/internal/link"
)

// Backend is the value-free operation surface of one genuinely accepted
// client/source invocation. Implementations must own and join their actual
// remote operations; a Host/Session DTO cannot implement a native producer.
type Backend interface {
	Exec(context.Context, string, io.Reader, io.Writer, io.Writer) (int, error)
	Shell(context.Context, Terminal, io.Reader, io.Writer, io.Writer) (TerminalProcess, error)
	ExecTerminal(context.Context, string, Terminal, io.Reader, io.Writer, io.Writer) (TerminalProcess, error)
	SFTP(context.Context) (io.ReadWriteCloser, error)
	Port(context.Context, uint16) (io.ReadWriteCloser, error)
	Close(context.Context) error
}

type TerminalProcess interface {
	Resize(context.Context, uint32, uint32) error
	Signal(context.Context, string) error
	Wait() (int, error)
	Close() error
}

type server struct {
	backend Backend
	ctx     context.Context
	workers sync.WaitGroup
	active  chan struct{}
}

// Serve owns standard input/output until completion. The caller has already
// selected the original source and must keep its key alive until Serve joins.
func Serve(ctx context.Context, input, output *os.File, key ssh.Signer, backend Backend) (returned error) {
	if ctx == nil || input == nil || output == nil || key == nil || backend == nil {
		return fmt.Errorf("SSH stdio server requires its owned transport, host key and actual backend")
	}
	owned, cancel := context.WithCancel(ctx)
	s := &server{backend: backend, ctx: owned, active: make(chan struct{}, link.DefaultMaxGuestStreams)}
	transport := &stdioConnection{input: input, output: output}
	stop := context.AfterFunc(owned, func() { _ = transport.Close() })
	defer stop()
	defer func() {
		cancel()
		_ = transport.Close()
		s.workers.Wait()
		cleanup, end := context.WithTimeout(context.Background(), 30*time.Second)
		defer end()
		returned = errors.Join(returned, backend.Close(cleanup))
	}()
	config := &ssh.ServerConfig{NoClientAuth: true, MaxAuthTries: 1,
		NoClientAuthCallback: func(peer ssh.ConnMetadata) (*ssh.Permissions, error) {
			if peer.User() != "agent" {
				return nil, fmt.Errorf("this SSH endpoint accepts only user agent")
			}
			return nil, nil
		}}
	config.AddHostKey(key)
	connection, channels, requests, err := ssh.NewServerConn(transport, config)
	if err != nil {
		return err
	}
	defer connection.Close()
	s.workers.Add(1)
	go func() {
		defer s.workers.Done()
		for request := range requests {
			_ = request.Reply(false, nil)
		}
	}()
	var rate openRate
	for incoming := range channels {
		if !rate.admit(time.Now()) {
			_ = incoming.Reject(ssh.ResourceShortage, "SSH channel-open rate exceeds 20 per second")
			continue
		}
		if incoming.ChannelType() != "session" && incoming.ChannelType() != "direct-tcpip" {
			_ = incoming.Reject(ssh.UnknownChannelType, "unsupported SSH channel type")
			continue
		}
		select {
		case s.active <- struct{}{}:
		default:
			_ = incoming.Reject(ssh.ResourceShortage, "the existing stream admission limit is occupied")
			continue
		}
		if incoming.ChannelType() == "direct-tcpip" {
			port, err := decodeDirectTCP(incoming.ExtraData())
			if err != nil {
				<-s.active
				_ = incoming.Reject(ssh.Prohibited, err.Error())
				continue
			}
			s.workers.Add(1)
			go func() {
				defer s.workers.Done()
				defer func() { <-s.active }()
				stream, err := backend.Port(owned, port)
				if err != nil || stream == nil {
					message := "the live guest port was refused"
					if err != nil {
						message = err.Error()
					}
					_ = incoming.Reject(ssh.Prohibited, message)
					return
				}
				s.servePort(incoming, stream)
			}()
			continue
		}

		if len(incoming.ExtraData()) != 0 {
			<-s.active
			_ = incoming.Reject(ssh.Prohibited, "session channels take no additional declaration")
			continue
		}
		channel, channelRequests, err := incoming.Accept()
		if err != nil {
			<-s.active
			continue
		}
		s.workers.Add(1)
		go func() {
			defer s.workers.Done()
			defer func() { <-s.active }()
			s.serveSession(channel, channelRequests)
		}()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}
