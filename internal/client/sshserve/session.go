package sshserve

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/proto"
	"golang.org/x/crypto/ssh"
)

type operationResult struct {
	code int
	err  error
}

type terminalLaunch struct {
	process   TerminalProcess
	request   *ssh.Request
	installed chan struct{}
}

func (s *server) serveSession(channel ssh.Channel, requests <-chan *ssh.Request) {
	ctx, cancel := context.WithCancel(s.ctx)
	defer cancel()
	defer channel.Close()
	interrupted := context.AfterFunc(ctx, func() { _ = channel.Close() })
	defer interrupted()
	var terminal *Terminal
	var process TerminalProcess
	var started bool
	var worker sync.WaitGroup
	defer func() {
		cancel()
		_ = channel.Close()
		if process != nil {
			_ = process.Close()
		}
		worker.Wait()
	}()
	finished := make(chan operationResult, 1)
	launched := make(chan terminalLaunch, 1)
	for {
		select {
		case running := <-launched:
			process = running.process
			_ = running.request.Reply(true, nil)
			close(running.installed)
		case result := <-finished:
			if result.err != nil {
				_, _ = fmt.Fprintln(channel.Stderr(), "The sandbox operation failed. Try: check the session through the client and retry.")
			}
			code := max(0, min(result.code, 255))
			_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Code uint32 }{uint32(code)}))
			return
		case request, open := <-requests:
			if !open {
				return
			}
			switch request.Type {
			case "pty-req":
				if started || terminal != nil {
					_ = request.Reply(false, nil)
					continue
				}
				parsed, err := decodeTerminal(request.Payload)
				if err == nil {
					terminal = &parsed
				}
				_ = request.Reply(err == nil, nil)
			case "window-change":
				var window struct{ Columns, Rows, Width, Height uint32 }
				valid := process != nil && terminal != nil && len(request.Payload) == 16 && ssh.Unmarshal(request.Payload, &window) == nil && window.Columns > 0 && window.Rows > 0 && window.Columns <= 1000 && window.Rows <= 1000
				if valid {
					valid = process.Resize(ctx, window.Columns, window.Rows) == nil
				}
				_ = request.Reply(valid, nil)
			case "signal":
				name, err := decodeSignal(request.Payload)
				valid := process != nil && err == nil
				if valid {
					valid = process.Signal(ctx, name) == nil
				}
				_ = request.Reply(valid, nil)
			case "shell":
				if started || len(request.Payload) != 0 {
					_ = request.Reply(false, nil)
					continue
				}
				started = true
				selected := Terminal{Term: "xterm-256color", Columns: 80, Rows: 24, Modes: "\x00"}
				if terminal != nil {
					selected = *terminal
				}
				terminal = &selected
				worker.Add(1)
				go func() {
					defer worker.Done()
					owned, err := s.backend.Shell(ctx, selected, channel, channel, channel.Stderr())
					if err != nil || owned == nil {
						if err == nil {
							err = fmt.Errorf("the terminal producer returned no owned process")
						}
						_ = request.Reply(false, nil)
						finished <- operationResult{125, err}
						return
					}
					defer owned.Close()
					abort := context.AfterFunc(ctx, func() { _ = owned.Close() })
					defer abort()
					installed := make(chan struct{})
					launched <- terminalLaunch{owned, request, installed}
					select {
					case <-installed:
					case <-ctx.Done():
						return
					}
					code, err := owned.Wait()
					finished <- operationResult{code, err}
				}()
			case "exec":
				var command struct{ Value string }
				if started || len(request.Payload) > proto.EditorSSHCommandMaxV1+4 || ssh.Unmarshal(request.Payload, &command) != nil || command.Value == "" || !utf8.ValidString(command.Value) || strings.ContainsRune(command.Value, 0) {
					_ = request.Reply(false, nil)
					continue
				}
				started = true
				if terminal != nil {
					selected := *terminal
					worker.Add(1)
					go func() {
						defer worker.Done()
						owned, err := s.backend.ExecTerminal(ctx, command.Value, selected, channel, channel, channel.Stderr())
						if err != nil || owned == nil {
							if err == nil {
								err = fmt.Errorf("the terminal producer returned no owned process")
							}
							_ = request.Reply(false, nil)
							finished <- operationResult{125, err}
							return
						}
						defer owned.Close()
						abort := context.AfterFunc(ctx, func() { _ = owned.Close() })
						defer abort()
						installed := make(chan struct{})
						launched <- terminalLaunch{owned, request, installed}
						select {
						case <-installed:
						case <-ctx.Done():
							return
						}
						code, err := owned.Wait()
						finished <- operationResult{code, err}
					}()
					continue
				}
				_ = request.Reply(true, nil)
				worker.Add(1)
				go func() {
					defer worker.Done()
					code, err := s.backend.Exec(ctx, command.Value, channel, channel, channel.Stderr())
					finished <- operationResult{code, err}
				}()
			case "subsystem":
				var subsystem struct{ Name string }
				if started || terminal != nil || len(request.Payload) > 128 || ssh.Unmarshal(request.Payload, &subsystem) != nil || subsystem.Name != "sftp" {
					_ = request.Reply(false, nil)
					continue
				}
				stream, err := s.backend.SFTP(ctx)
				if err != nil || stream == nil {
					_ = request.Reply(false, nil)
					continue
				}
				started = true
				_ = request.Reply(true, nil)
				worker.Add(1)
				go func() { defer worker.Done(); copyDuplex(ctx, channel, stream); finished <- operationResult{} }()
			default:
				// Includes env, X11, agent forwarding and all extensions.
				_ = request.Reply(false, nil)
			}
		case <-ctx.Done():
			return
		}
	}
}

func (s *server) servePort(incoming ssh.NewChannel, stream io.ReadWriteCloser) {
	defer stream.Close()
	channel, requests, err := incoming.Accept()
	if err != nil {
		return
	}
	defer channel.Close()
	var replies sync.WaitGroup
	replies.Add(1)
	go func() {
		defer replies.Done()
		for request := range requests {
			_ = request.Reply(false, nil)
		}
	}()
	copyDuplex(s.ctx, channel, stream)
	_ = channel.Close()
	replies.Wait()
}

func copyDuplex(ctx context.Context, channel ssh.Channel, stream io.ReadWriteCloser) {
	stop := context.AfterFunc(ctx, func() { _ = channel.Close(); _ = stream.Close() })
	defer stop()
	var input sync.WaitGroup
	input.Add(1)
	go func() {
		defer input.Done()
		_, _ = io.Copy(stream, channel)
		if half, ok := stream.(interface{ CloseWrite() error }); ok {
			_ = half.CloseWrite()
		} else {
			_ = stream.Close()
		}
	}()
	_, _ = io.Copy(channel, stream)
	_ = channel.CloseWrite()
	_ = channel.Close()
	_ = stream.Close()
	input.Wait()
}
