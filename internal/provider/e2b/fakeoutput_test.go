package e2b

import (
	"errors"
	"io"
	"os"
	"sync"
	"time"
)

// fakeOut keeps a pipe's queued bytes when a signal ends the stream. cut
// wakes an active Read, saves what remains and then lets the reader reach EOF.
type fakeOut struct {
	mu      sync.Mutex
	cond    *sync.Cond
	f       *os.File
	buf     []byte
	reading bool
	cutReq  bool
}

func newFakeOut(f *os.File) *fakeOut {
	o := &fakeOut{f: f}
	o.cond = sync.NewCond(&o.mu)
	return o
}

func (o *fakeOut) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	o.mu.Lock()
	for {
		for o.reading || o.cutReq && o.f != nil {
			o.cond.Wait()
		}
		if o.f == nil {
			if len(o.buf) == 0 {
				o.mu.Unlock()
				return 0, io.EOF
			}
			n := copy(p, o.buf)
			o.buf = o.buf[n:]
			if len(o.buf) == 0 {
				o.buf = nil
			}
			o.mu.Unlock()
			return n, nil
		}
		f := o.f
		o.reading = true
		o.mu.Unlock()
		n, err := f.Read(p)
		o.mu.Lock()
		o.reading = false
		cut := o.cutReq
		o.cond.Broadcast()
		if n > 0 {
			if cut && errors.Is(err, os.ErrDeadlineExceeded) {
				err = nil
			}
			o.mu.Unlock()
			return n, err
		}
		if cut && errors.Is(err, os.ErrDeadlineExceeded) {
			continue
		}
		o.mu.Unlock()
		return 0, err
	}
}

func (o *fakeOut) cut() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.f == nil {
		return
	}
	o.cutReq = true
	if o.reading {
		_ = o.f.SetReadDeadline(time.Unix(1, 0))
		for o.reading {
			o.cond.Wait()
		}
	}
	if o.f == nil {
		return
	}
	_ = o.f.SetReadDeadline(time.Time{})
	drainPipe(o.f, &o.buf)
	_ = o.f.Close()
	o.f = nil
	o.cond.Broadcast()
}

func (o *fakeOut) Close() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.cutReq = true
	if o.f != nil && o.reading {
		_ = o.f.SetReadDeadline(time.Unix(1, 0))
		for o.reading {
			o.cond.Wait()
		}
	}
	var err error
	if o.f != nil {
		err = o.f.Close()
		o.f = nil
	}
	o.buf = nil
	o.cond.Broadcast()
	return err
}
