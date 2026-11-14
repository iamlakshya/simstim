//go:build darwin || linux

package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/muesli/cancelreader"
	"golang.org/x/term"
)

type fastProbeReplies struct {
	queued        chan []byte
	stop          chan struct{}
	stopOnce      sync.Once
	fd            int
	raw           *term.State
	readBeforeRaw atomic.Bool
	closeWasRaw   atomic.Bool
	reads         atomic.Int32
}

func (r *fastProbeReplies) Read(p []byte) (int, error) {
	r.reads.Add(1)
	state, err := term.GetState(r.fd)
	if err != nil || !reflect.DeepEqual(state, r.raw) {
		r.readBeforeRaw.Store(true)
	}
	select {
	case b := <-r.queued:
		return copy(p, b), nil
	case <-r.stop:
		return 0, io.EOF
	}
}

func (r *fastProbeReplies) Cancel() bool {
	r.stopOnce.Do(func() { close(r.stop) })
	return true
}

// The factory models FlushConsoleInputBuffer. If queries came first, it
// deterministically waits for their fast replies and discards them. A reader
// made before the queries instead receives every reply after raw mode starts.
func TestTerminalProbeOpensItsReaderBeforeSendingQueries(t *testing.T) {
	in, out, original := probeTerminalFiles(t)
	fd := int(in.TTY.Fd())
	if _, err := term.MakeRaw(fd); err != nil {
		t.Fatal(err)
	}
	raw, err := term.GetState(fd)
	if err != nil {
		t.Fatal(err)
	}
	if err := term.Restore(fd, original); err != nil {
		t.Fatal(err)
	}
	r := &fastProbeReplies{queued: make(chan []byte, 1), stop: make(chan struct{}), fd: fd, raw: raw}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	querySeen := make(chan struct{})
	controllerDone := make(chan struct{})
	go func() {
		defer close(controllerDone)
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			if bytes.Contains(out.Output(), []byte(probeQueries)) {
				r.queued <- []byte("\x1bP>|FastFixtureTerm 1.0\x1b\\\x1b[?1u\x1b[?62;22c")
				close(querySeen)
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	t.Cleanup(func() { cancel(); r.Cancel(); <-controllerDone })
	var factoryBeforeRaw atomic.Bool
	newKeyReader = func(*os.File) (cancelreader.CancelReader, error) {
		before, err := term.GetState(fd)
		if err != nil {
			return nil, err
		}
		factoryBeforeRaw.Store(reflect.DeepEqual(before, original))
		if !factoryBeforeRaw.Load() {
			select {
			case <-querySeen:
			case <-ctx.Done():
				return nil, errors.New("queries never reached the late reader factory")
			}
		}
		select {
		case <-r.queued:
		default:
		}
		return preparedConsoleReader{
			Reader: r,
			cancel: r.Cancel,
			close: func() error {
				atClose, _ := term.GetState(fd)
				r.closeWasRaw.Store(reflect.DeepEqual(atClose, raw))
				return term.Restore(fd, before)
			},
		}, nil
	}
	if err := terminalProbe(ctx); err != nil {
		t.Fatal(err)
	}
	if !factoryBeforeRaw.Load() || r.readBeforeRaw.Load() || r.reads.Load() != 1 || !r.closeWasRaw.Load() {
		t.Errorf("reader order: factory-before-raw=%v early-read=%v reads=%d close-before-restore=%v", factoryBeforeRaw.Load(), r.readBeforeRaw.Load(), r.reads.Load(), r.closeWasRaw.Load())
	}
	got := string(finishedProbeOutput(t, out))
	if !strings.Contains(got, "FastFixtureTerm 1.0") || !strings.Contains(got, "supported: Shift+Enter") {
		t.Errorf("fast terminal replies were lost:\n%s", got)
	}
	after, err := term.GetState(fd)
	if err != nil || !reflect.DeepEqual(original, after) {
		t.Errorf("fast-reply probe left changed modes: %v", err)
	}
}
