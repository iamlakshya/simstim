package keyread

import (
	"fmt"
	"time"
)

// start starts a read of up to n bytes, unless one is in flight. r.mu is
// held, and r is not closed. The read goroutine starts at the first read,
// and serves every read after it, so a paste of a thousand reads starts no
// thousand goroutines.
func (r *Reader) start(n int) {
	if r.inFlight {
		return
	}
	r.inFlight, r.reading = true, true
	if r.want == nil {
		r.want, r.readDone = make(chan int, 1), make(chan struct{})
		go r.reads(r.want, r.readDone)
	}
	r.want <- n
}

// reads is a Reader's one read goroutine. For each n from want, it reads
// up to n bytes into its one buffer, and sends them on res. The bytes stay
// as they are until the next n, which comes only once the bytes have been
// used or copied (took, block). A read that fails ends the goroutine, as
// a closed want does: the next read starts another.
func (r *Reader) reads(want <-chan int, done chan struct{}) {
	defer close(done)
	buf := make([]byte, pasteBlock)
	for n := range want {
		for {
			k, err := r.src.Read(buf[:n])
			if err != nil {
				r.mu.Lock()
				r.reading = false
				if r.want == want {
					r.want, r.readDone = nil, nil
				}
				r.mu.Unlock()
				r.res <- result{buf[:k], err}
				return
			}
			if k > 0 {
				r.mu.Lock()
				r.reading = false
				r.mu.Unlock()
				r.res <- result{buf[:k], nil}
				break
			}
		}
	}
}

// end cancels a read of r that waits, waits for its read goroutine to
// exit, and closes r. When it cannot end the read, it returns the read
// goroutine's done channel alongside the error so callers can close r.src
// only once that read has finished.
func (r *Reader) end() (<-chan struct{}, error) {
	r.mu.Lock()
	reading, want, done := r.reading, r.want, r.readDone
	r.closed = true
	if want != nil {
		close(want)
		r.want, r.readDone = nil, nil
	}
	r.mu.Unlock()
	if reading && !r.src.Cancel() {
		r.mu.Lock()
		still := r.reading
		r.mu.Unlock()
		if still {
			return done, ErrCannotCancel
		}
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(ReleaseWait):
			return done, ErrReadGoesOn
		}
	}
	_ = r.src.Close()
	return nil, nil
}

// took ends the read in flight with res, and returns its bytes. A read
// with bytes counts, also with an error: the next read gives the error.
func (r *Reader) took(res result) ([]byte, error) {
	r.mu.Lock()
	r.inFlight = false
	r.mu.Unlock()
	if len(res.data) == 0 {
		return nil, fmt.Errorf("read the terminal: %w", res.err)
	}
	return res.data, nil
}
