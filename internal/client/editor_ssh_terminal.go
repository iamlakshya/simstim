package client

import (
	"context"
	"io"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/client/sshserve"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

type editorTerminalResultV1 struct {
	code int
	err  error
}
type editorTerminalV1 struct {
	stream *link.Stream
	wire   *link.EditorTerminalV1
	stop   func() bool
	done   chan struct{}
	result editorTerminalResultV1
	once   sync.Once
}

func (i *editorSSHInvocationV1) Shell(ctx context.Context, terminal sshserve.Terminal, input io.Reader, stdout, stderr io.Writer) (sshserve.TerminalProcess, error) {
	return i.execTerminal(ctx, "", terminal, input, stdout, stderr)
}
func (i *editorSSHInvocationV1) ExecTerminal(ctx context.Context, command string, terminal sshserve.Terminal, input io.Reader, stdout, stderr io.Writer) (sshserve.TerminalProcess, error) {
	return i.execTerminal(ctx, command, terminal, input, stdout, stderr)
}
func (i *editorSSHInvocationV1) execTerminal(ctx context.Context, command string, terminal sshserve.Terminal, input io.Reader, stdout, _ io.Writer) (sshserve.TerminalProcess, error) {
	stream, err := i.open(ctx, link.KindHolder, proto.EditorChannelV1{Version: 1, Command: command, Term: terminal.Term, Cols: terminal.Columns, Rows: terminal.Rows, Modes: []byte(terminal.Modes)})
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = stream.SetDeadline(time.Now()); _ = stream.Close() })
	if err = readEditorReadyContextV1(ctx, stream); err != nil {
		stop()
		_ = stream.Close()
		return nil, err
	}
	process := &editorTerminalV1{stream: stream, wire: link.NewEditorTerminalV1(stream), stop: stop, done: make(chan struct{})}
	i.workers.Add(2)
	go func() {
		defer i.workers.Done()
		if input != nil {
			if _, err := io.Copy(editorTerminalInputV1{process.wire}, input); err != nil {
				return
			}
		}
		_ = process.wire.Control(proto.TRequest, proto.EditorTerminalControlV1{InputEOF: true})
	}()
	go func() {
		defer i.workers.Done()
		defer close(process.done)
		process.result = process.read(stdout)
	}()
	return process, nil
}

type editorTerminalInputV1 struct{ wire *link.EditorTerminalV1 }

func (w editorTerminalInputV1) Write(data []byte) (int, error) {
	return w.wire.WriteData(proto.TInput, data)
}

func (p *editorTerminalV1) read(stdout io.Writer) editorTerminalResultV1 {
	if stdout == nil {
		stdout = io.Discard
	}
	for {
		frame, err := p.wire.Read()
		if err != nil {
			return editorTerminalResultV1{125, err}
		}
		switch frame.Type {
		case proto.TOutput:
			if _, err = stdout.Write(frame.Payload); err != nil {
				return editorTerminalResultV1{125, err}
			}
		case proto.TEnded:
			var ended proto.Ended
			if link.DecodeEditorControlV1(frame.Payload, &ended) != nil || ended.ExitCode < 0 || ended.ExitCode > 255 {
				return editorTerminalResultV1{125, sandboxReceiptError()}
			}
			var extra [1]byte
			if n, err := p.stream.Read(extra[:]); n != 0 || err != io.EOF {
				return editorTerminalResultV1{125, sandboxReceiptError()}
			}
			return editorTerminalResultV1{ended.ExitCode, nil}
		default:
			return editorTerminalResultV1{125, sandboxReceiptError()}
		}
	}
}
func (p *editorTerminalV1) Resize(ctx context.Context, cols, rows uint32) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if cols == 0 || rows == 0 || cols > 1000 || rows > 1000 {
		return sandboxReceiptError()
	}
	return p.wire.Control(proto.TResize, proto.Resize{Cols: int(cols), Rows: int(rows)})
}
func (p *editorTerminalV1) Signal(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	number, ok := proto.EditorSignalNumberV1(name)
	if !ok {
		return sandboxReceiptError()
	}
	return p.wire.Control(proto.TRequest, proto.EditorTerminalControlV1{Signal: number})
}
func (p *editorTerminalV1) Wait() (int, error) { <-p.done; return p.result.code, p.result.err }
func (p *editorTerminalV1) Close() error {
	p.once.Do(func() { p.stop(); _ = p.stream.SetDeadline(time.Now()); _ = p.stream.Close(); <-p.done })
	return nil
}
