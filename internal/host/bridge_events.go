package host

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

// bridgeStdioConn wraps the bridge's buffered stdin reader and raw stdout
// writer as a net.Conn so the bridge can serve streaming ops (such as
// `events.watch`) directly over internal/link (isolation spec 10.5).
type bridgeStdioConn struct {
	r    *bufio.Reader
	w    io.Writer
	once sync.Once
	done chan struct{}
}

func newBridgeStdioConn(r *bufio.Reader, w io.Writer) *bridgeStdioConn {
	return &bridgeStdioConn{r: r, w: w, done: make(chan struct{})}
}

func (c *bridgeStdioConn) Read(p []byte) (int, error)  { return c.r.Read(p) }  //nolint:wrapcheck // net.Conn pass-through
func (c *bridgeStdioConn) Write(p []byte) (int, error) { return c.w.Write(p) } //nolint:wrapcheck // net.Conn pass-through
func (c *bridgeStdioConn) Close() error {
	c.once.Do(func() { close(c.done) })
	return nil
}
func (c *bridgeStdioConn) LocalAddr() net.Addr              { return bridgeAddr("bridge") }
func (c *bridgeStdioConn) RemoteAddr() net.Addr             { return bridgeAddr("client") }
func (c *bridgeStdioConn) SetDeadline(time.Time) error      { return nil }
func (c *bridgeStdioConn) SetReadDeadline(time.Time) error  { return nil }
func (c *bridgeStdioConn) SetWriteDeadline(time.Time) error { return nil }

type bridgeAddr string

func (a bridgeAddr) Network() string { return "pipe" }
func (a bridgeAddr) String() string  { return string(a) }

// serveEventsWatch serves the streaming `events.watch` bridge op directly in
// the bridge using link.KindEvents and link.BridgeOpener (spec 10.5, 17.6).
func (b *bridge) serveEventsWatch(req proto.Request, r *bufio.Reader, out io.Writer) bool {
	if err := CheckCaller(); err != nil {
		b.reply(req.ID, nil, err)
		return false
	}
	var args api.EventsWatchArgs
	if len(req.Args) > 0 {
		if err := json.Unmarshal(req.Args, &args); err != nil {
			b.reply(req.ID, nil, UserError("bad_request", "the events.watch request is not valid JSON", "Update the client."))
			return false
		}
	}
	if args.Session != "" && !validID(args.Session) {
		b.reply(req.ID, nil, UserError("not_found", "no session "+args.Session, "List sessions with ls."))
		return false
	}
	// Snapshot the current log position before confirming OK so events written
	// immediately after the client's events.watch handshake completes are never
	// skipped when Follow is true.
	logPath := paths.EventsLog()
	var startOffset int64
	var startInode uint64
	if args.Follow {
		_ = paths.EnsurePrivateDir(filepath.Dir(logPath))
		if fi, err := os.Stat(logPath); err == nil {
			startOffset = fi.Size()
			startInode = fileInode(fi)
		}
	}

	if err := b.w.WriteJSON(proto.TResponse, proto.Response{ID: req.ID, OK: true}); err != nil {
		return true
	}

	conn := newBridgeStdioConn(r, out)
	cliLink := link.New(conn, link.Supervisor, link.Options{
		Opener:               link.BridgeOpener,
		AllowVersionMismatch: true,
		MaxSupervisorStreams: 4,
		MaxGuestStreams:      1,
	})
	defer cliLink.Close()

	octx, ocancel := context.WithTimeout(context.Background(), 10*time.Second)
	st, err := cliLink.Open(octx, link.KindEvents, args)
	ocancel()
	if err != nil {
		return true
	}
	defer st.Close()

	if !args.Follow {
		evs, _ := ReadWatchEvents(args.Session, args.Limit)
		for _, ev := range evs {
			line, merr := json.Marshal(ev)
			if merr != nil {
				continue
			}
			if _, werr := st.Write(append(line, '\n')); werr != nil {
				return true
			}
		}
		_ = st.CloseWrite()
		return true
	}

	b.streamEventsFollow(cliLink, st, args.Session, logPath, startOffset, startInode)
	return true
}

type eventsTailer struct {
	st        *link.Stream
	sessionID string
	logPath   string
	offset    int64
	inode     uint64
	partial   []byte
}

func (b *bridge) streamEventsFollow(cliLink *link.Session, st *link.Stream, sessionID, logPath string, curOffset int64, curInode uint64) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-cliLink.Done()
		cancel()
	}()
	// Also stop if the client closes its end of the events stream.
	go func() {
		buf := make([]byte, 64)
		for {
			if _, err := st.Read(buf); err != nil {
				cancel()
				return
			}
		}
	}()

	tail := &eventsTailer{
		st:        st,
		sessionID: sessionID,
		logPath:   logPath,
		offset:    curOffset,
		inode:     curInode,
	}
	_ = watchEventsFile(ctx, logPath, tail.drain)
}

func (t *eventsTailer) drain() error {
	fi, err := os.Stat(t.logPath)
	if err != nil {
		return nil //nolint:nilerr // log file may not exist yet or be mid-rotation
	}
	ino := fileInode(fi)
	if (t.inode != 0 && ino != 0 && ino != t.inode) || fi.Size() < t.offset {
		t.offset = 0
		t.inode = ino
		t.partial = nil
	}
	if fi.Size() == t.offset {
		return nil
	}
	data, err := t.readChunk(ino)
	if err != nil || len(data) == 0 {
		return nil //nolint:nilerr // transient open/read error during rotation; retry on next event
	}
	t.partial = append(t.partial, data...)
	return t.flushLines()
}

func (t *eventsTailer) readChunk(ino uint64) ([]byte, error) {
	f, err := os.Open(t.logPath)
	if err != nil {
		return nil, fmt.Errorf("open events log: %w", err)
	}
	defer f.Close()
	if _, err := f.Seek(t.offset, io.SeekStart); err != nil {
		return nil, fmt.Errorf("seek events log: %w", err)
	}
	data, err := io.ReadAll(f)
	if err != nil && len(data) == 0 {
		return nil, fmt.Errorf("read events log: %w", err)
	}
	t.offset += int64(len(data))
	t.inode = ino
	return data, nil
}

func (t *eventsTailer) flushLines() error {
	for {
		idx := bytes.IndexByte(t.partial, '\n')
		if idx < 0 {
			return nil
		}
		line := bytes.TrimSpace(t.partial[:idx])
		t.partial = t.partial[idx+1:]
		if len(line) == 0 {
			continue
		}
		if t.sessionID != "" && !eventMatchesSession(line, t.sessionID) {
			continue
		}
		if _, err := t.st.Write(append(line, '\n')); err != nil {
			return err
		}
	}
}

func eventMatchesSession(line []byte, sessionID string) bool {
	var ev api.WatchEvent
	return json.Unmarshal(line, &ev) == nil && ev.Session == sessionID
}
