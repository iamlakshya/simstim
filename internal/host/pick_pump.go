package host

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/keys"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/term"
	"github.com/iamlakshya/simstim/internal/version"
)

// pickEscFlush is how long the input filter may hold an unfinished key
// sequence before it goes to the session as it is. A lone ESC never waits.
const pickEscFlush = 15 * time.Millisecond

// pickDetach is the key that detaches. Phones send it as the legacy byte.
var pickDetach = keys.Key{Code: '\\', Mods: keys.ModCtrl}

// pickPump carries one attach between a terminal and a holder's socket. The
// session's bytes reach the terminal unchanged; only Ctrl+\ is kept back.
type pickPump struct {
	conn  io.ReadWriter
	out   io.Writer
	size  func() (cols, rows int)
	winch <-chan os.Signal

	w       *proto.Writer
	filter  *keys.Filter
	tracker *term.Tracker
	cu      term.Catchup
}

// pickEnd says how an attach ended: the user detached, or the program ended.
type pickEnd struct {
	ended *proto.Ended
}

// run attaches to session id and pumps until the user detaches, the input
// ends or the session ends. It then undoes the modes the session turned on in
// the terminal, without RIS, so the scrollback stays.
func (p *pickPump) run(id string, input <-chan []byte) (pickEnd, error) {
	r := bufio.NewReaderSize(p.conn, 64<<10)
	p.w = proto.NewWriter(p.conn)
	p.filter = keys.New(pickDetach)
	p.tracker = term.NewTracker()
	defer func() { _, _ = p.out.Write(p.tracker.M.ResetSequence()) }()
	if err := p.hello(r); err != nil {
		return pickEnd{}, err
	}
	cols, rows := p.size()
	if err := p.w.WriteJSON(proto.TAttach, proto.Attach{Session: id, Cols: cols, Rows: rows, Client: brand.Name + "-pick", LastOffset: -1}); err != nil {
		return pickEnd{}, fmt.Errorf("attach: %w", err)
	}
	frames := make(chan proto.Frame, 64)
	quit := make(chan struct{})
	defer close(quit)
	go func() {
		defer close(frames)
		for {
			f, err := proto.Read(r)
			if err != nil {
				return
			}
			select {
			case frames <- f:
			case <-quit:
				return
			}
		}
	}()
	var flush <-chan time.Time
	for {
		select {
		case f, ok := <-frames:
			if !ok {
				return pickEnd{}, errors.New("the session's holder went away")
			}
			if end, done, err := p.frame(f); done || err != nil {
				return end, err
			}
		case b, ok := <-input:
			if !ok || p.keys(b) {
				return pickEnd{}, nil
			}
			flush = nil
			if p.filter.Pending() {
				flush = time.After(pickEscFlush)
			}
		case <-flush:
			flush = nil
			if b := p.filter.Flush(); len(b) > 0 {
				_ = p.w.Write(proto.TInput, b)
			}
		case <-p.winch:
			c, r := p.size()
			_ = p.w.WriteJSON(proto.TResize, proto.Resize{Cols: c, Rows: r})
		}
	}
}

func (p *pickPump) hello(r *bufio.Reader) error {
	if err := p.w.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: "pick"}); err != nil {
		return fmt.Errorf("hello: %w", err)
	}
	f, err := proto.Read(r)
	if err != nil || f.Type != proto.THello {
		return errors.New("the session's holder did not answer")
	}
	return nil
}

// frame handles one frame from the holder. done is true when the attach is over.
func (p *pickPump) frame(f proto.Frame) (end pickEnd, done bool, err error) {
	switch f.Type {
	case proto.TAttached:
		var att proto.Attached
		if err := json.Unmarshal(f.Payload, &att); err != nil {
			return end, true, fmt.Errorf("attach: %w", err)
		}
		p.cu.Offset = att.From
		p.cu.History(att.Live)
		// Keep what the terminal shows in its scrollback, then start clean.
		_, rows := p.size()
		p.write([]byte(fmt.Sprintf("\x1b[%d;1H%s\x1b[H", rows, newlines(rows))))
	case proto.TSnapshot:
		p.write(f.Payload)
		p.tracker.Feed(f.Payload)
	case proto.TOutput:
		// A frame that does not decode is skipped; the next one goes on.
		if off, data, derr := proto.DecodeOutput(f.Payload); derr == nil {
			if show, fresh := p.cu.Feed(off, data); fresh != nil {
				p.write(show)
				p.tracker.Feed(fresh)
			}
		}
	case proto.TEnded:
		var e proto.Ended
		_ = json.Unmarshal(f.Payload, &e)
		return pickEnd{ended: &e}, true, nil
	case proto.TError:
		var pe proto.Error
		_ = json.Unmarshal(f.Payload, &pe)
		return end, true, &pe
	default:
		// Other frames do not change the terminal.
	}
	return end, false, nil
}

// keys sends typed bytes to the session, and reports true on the detach key.
func (p *pickPump) keys(b []byte) (detach bool) {
	for _, pc := range p.filter.Feed(b) {
		switch {
		case pc.Kind == keys.Match && !pc.Release && !pc.Repeat:
			return true
		case pc.Kind == keys.Match:
			// The release of the detach key: it belongs to us, not the session.
		default:
			_ = p.w.Write(proto.TInput, pc.Raw)
		}
	}
	return false
}

func (p *pickPump) write(b []byte) {
	if len(b) > 0 {
		_, _ = p.out.Write(b)
	}
}

func newlines(n int) string {
	b := make([]byte, max(n, 0))
	for i := range b {
		b[i] = '\n'
	}
	return string(b)
}
