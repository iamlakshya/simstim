package tui

import (
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/muesli/cancelreader"

	"github.com/iamlakshya/simstim/internal/keyread"
)

var (
	openDemoAttachReader  = cancelreader.NewReader
	demoAttachReleaseWait = 5 * time.Second
	demoReleaseAfter      = time.After
)

// demoInput starts another read only after the agent has handled the last
// chunk. Detach therefore leaves no pending read on an uncancellable console.
type demoInput struct {
	in       *os.File
	cr       cancelreader.CancelReader
	keys     chan []byte
	wantRead chan struct{}
	stop     chan struct{}
	readDone chan struct{}

	mu       sync.Mutex
	reading  bool
	stopping bool
}

// startDemoInput leaves the first read parked until the caller enters raw mode.
func startDemoInput(in *os.File) (*demoInput, error) {
	cr, err := openDemoAttachReader(in)
	if err != nil {
		return nil, fmt.Errorf("read the terminal: %w", err)
	}
	d := &demoInput{in: in, cr: cr, keys: make(chan []byte), wantRead: make(chan struct{}, 1), stop: make(chan struct{}), readDone: make(chan struct{})}
	go d.read()
	return d, nil
}

func (d *demoInput) read() {
	defer close(d.readDone)
	defer close(d.keys)
	buf := make([]byte, 256)
	for {
		select {
		case <-d.wantRead:
		case <-d.stop:
			return
		}
		d.mu.Lock()
		if d.stopping {
			d.mu.Unlock()
			return
		}
		d.reading = true
		d.mu.Unlock()
		n, err := d.cr.Read(buf)
		d.mu.Lock()
		d.reading = false
		d.mu.Unlock()
		if n > 0 {
			select {
			case d.keys <- append([]byte(nil), buf[:n]...):
			case <-d.stop:
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func (d *demoInput) ask() {
	select {
	case d.wantRead <- struct{}{}:
	default:
	}
}

func (d *demoInput) finish() bool {
	d.mu.Lock()
	d.stopping = true
	reading := d.reading
	d.mu.Unlock()
	close(d.stop)
	if !reading {
		<-d.readDone
		_ = d.cr.Close()
		return false
	}
	var cause error
	if d.cr.Cancel() {
		select {
		case <-d.readDone:
			_ = d.cr.Close()
			return false
		case <-demoReleaseAfter(demoAttachReleaseWait):
			cause = keyread.ErrReadGoesOn
		}
	} else {
		select {
		case <-d.readDone:
			_ = d.cr.Close()
			return false
		default:
			cause = keyread.ErrCannotCancel
		}
	}
	keyread.HoldStuck(d.in, d.cr.Close, d.readDone, cause)
	return true
}
