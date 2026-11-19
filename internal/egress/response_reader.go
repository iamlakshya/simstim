package egress

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
)

const responseReadBuffer = 32 << 10

type responseEchoGetter func(context.Context) (*responseEchoSnapshot, error)
type responseMintRegister func(context.Context, api.Secret, string) (api.Secret, error)

var (
	errResponseRead     = errors.New("response stream failed. Try: repeat the request")
	errResponseRegistry = errors.New("response credential registration failed. Try: repeat the request or resume the session")
	errResponseOverlap  = errors.New("response credential spans overlap ambiguously. Try: review the session's secret bindings")
	errResponseBounds   = errors.New("response credential carry exceeds its bound. Try: resume the session")
)

// responseCredentialReader owns input.Close. It never starts a read goroutine.
// The supplied response scope must make Close promptly unblock its own Read,
// for example by closing the owned origin connection before its body. Close
// must not join this reader. No such promise is possible for arbitrary Readers.
type responseCredentialReader struct{ *responseReaderState }

// Only the immutable facade is returned as a derived value view. Its value
// Formatter copies this pointer, never the mutable buffers behind it.
type responseReaderState struct {
	mu          *sync.Mutex
	ctx         context.Context
	cancel      context.CancelFunc
	input       io.ReadCloser
	getEcho     responseEchoGetter
	echo        *responseEchoSnapshot
	rules       *responseRuleSnapshot
	register    responseMintRegister
	closeDone   chan struct{}
	closeErr    error
	buffer      []byte
	start       int
	maxEcho     int
	scratch     [responseReadBuffer]byte
	eof         bool
	previous    byte
	hasPrevious bool
	replacement api.Secret
	replaced    int
	terminal    error
	emptyReads  int
}

func newResponseCredentialReader(ctx context.Context, input io.ReadCloser, getEcho responseEchoGetter, rules *responseRuleSnapshot, register responseMintRegister) (*responseCredentialReader, error) {
	if ctx == nil || input == nil || getEcho == nil || rules != nil && len(rules.rules) > 0 && register == nil {
		return nil, errors.New("response credential reader is not configured. Try: update matching helpers")
	}
	owned, cancel := context.WithCancel(ctx)
	state := &responseReaderState{mu: new(sync.Mutex), ctx: owned, cancel: cancel, input: input, getEcho: getEcho, rules: rules, register: register, closeDone: make(chan struct{})}
	reader := &responseCredentialReader{responseReaderState: state}
	context.AfterFunc(owned, func() { reader.closeErr = input.Close(); close(reader.closeDone) })
	return reader, nil
}

func (*responseCredentialReader) String() string            { return "response credential reader" }
func (*responseCredentialReader) GoString() string          { return "response credential reader" }
func (responseCredentialReader) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, api.Redacted) }
func (*responseReaderState) Format(f fmt.State, _ rune)     { _, _ = io.WriteString(f, api.Redacted) }

func (r *responseCredentialReader) Close() error {
	r.cancel()
	<-r.closeDone
	r.mu.Lock()
	defer r.mu.Unlock()
	clear(r.buffer)
	r.buffer = nil
	r.start = 0
	r.replacement = ""
	r.replaced = 0
	clear(r.scratch[:])
	r.echo = nil
	r.getEcho = nil
	r.register = nil
	r.input = nil
	if r.terminal == nil {
		r.terminal = io.ErrClosedPipe
	}
	if r.closeErr != nil {
		return errResponseRead
	}
	return nil
}

func (r *responseCredentialReader) finish(err error) error {
	r.cancel()
	<-r.closeDone
	if err == io.EOF && r.closeErr != nil {
		err = errResponseRead
	}
	clear(r.buffer)
	r.buffer = nil
	r.start = 0
	r.replacement = ""
	r.replaced = 0
	clear(r.scratch[:])
	r.echo = nil
	r.getEcho = nil
	r.register = nil
	r.input = nil
	r.terminal = err
	return err
}

func (r *responseCredentialReader) patternWindow() int {
	if r.rules == nil {
		return responsePatternWindow
	}
	return r.rules.window
}

func (r *responseCredentialReader) refresh() error {
	if err := r.ctx.Err(); err != nil {
		return err
	}
	snapshot, err := r.getEcho(r.ctx)
	if err != nil {
		if r.ctx.Err() != nil {
			return r.ctx.Err()
		}
		return errResponseRegistry
	}
	r.echo = snapshot
	if snapshot != nil {
		r.maxEcho = max(r.maxEcho, snapshot.maxBytes)
	}
	if r.maxEcho > (int(^uint(0)>>1)-r.patternWindow()-responseReadBuffer)/2 {
		return errResponseBounds
	}
	return nil
}

func (r *responseCredentialReader) pending() []byte { return r.buffer[r.start:] }

func (r *responseCredentialReader) consume(n int) {
	data := r.pending()
	if n > 0 {
		r.previous = data[n-1]
		r.hasPrevious = true
		r.start += n
	}
}

func (r *responseCredentialReader) readMore() error {
	remaining := len(r.pending())
	if r.start > 0 {
		copy(r.buffer, r.pending())
		clear(r.buffer[remaining:])
		r.buffer = r.buffer[:remaining]
		r.start = 0
	}
	// A second echo may begin at the first span's last byte and extend a
	// full maxEcho. Two spans plus finite pattern context suffice to decide
	// every crossing before the first span is released.
	budget := 2*r.maxEcho + r.patternWindow() + responseReadBuffer
	want := min(responseReadBuffer, budget-remaining)
	if want <= 0 {
		return errResponseBounds
	}
	n, err := r.input.Read(r.scratch[:want])
	if n < 0 || n > want {
		return errResponseRead
	}
	if r.ctx.Err() != nil {
		clear(r.scratch[:n])
		return r.ctx.Err()
	}
	if err != nil && !errors.Is(err, io.EOF) {
		clear(r.scratch[:n])
		return errResponseRead
	}
	if n == 0 && err == nil {
		r.emptyReads++
		if r.emptyReads >= 100 {
			return io.ErrNoProgress
		}
		return nil
	}
	r.emptyReads = 0
	if n > 0 {
		length := remaining + n
		if length > cap(r.buffer) {
			growth := budget
			if cap(r.buffer) <= budget/2 {
				growth = 2 * cap(r.buffer)
			}
			capacity := min(budget, max(length, growth))
			next := make([]byte, length, capacity)
			copy(next, r.buffer)
			clear(r.buffer)
			r.buffer = next
		} else {
			r.buffer = r.buffer[:length]
		}
		copy(r.buffer[remaining:], r.scratch[:n])
		clear(r.scratch[:n])
	}
	if errors.Is(err, io.EOF) {
		r.eof = true
	}
	return r.refresh()
}

func (r *responseCredentialReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.terminal != nil {
		return 0, r.terminal
	}
	if err := r.refresh(); err != nil {
		return 0, r.finish(err)
	}
	n := 0
	for n < len(p) {
		if err := r.ctx.Err(); err != nil {
			return n, r.finish(err)
		}
		if r.replacement != "" {
			copied := copy(p[n:], r.replacement[r.replaced:])
			n += copied
			r.replaced += copied
			if r.replaced == len(r.replacement) {
				r.replacement = ""
				r.replaced = 0
			}
			continue
		}
		if len(r.pending()) == 0 {
			if r.eof {
				if n > 0 {
					return n, nil
				}
				return 0, r.finish(io.EOF)
			}
			if n > 0 {
				return n, nil
			}
			if err := r.readMore(); err != nil {
				return 0, r.finish(err)
			}
			continue
		}
		echo := r.echo.match(r.pending(), r.eof)
		mint := r.rules.match(r.pending(), r.eof, r.previous, r.hasPrevious)
		if echo.wait || mint.wait {
			if n > 0 {
				return n, nil
			}
			if err := r.readMore(); err != nil {
				return 0, r.finish(err)
			}
			continue
		}
		length := echo.length
		isMint := mint.length > length
		if isMint {
			length = mint.length
		}
		if !isMint && echo.err != nil {
			return n, r.finish(echo.err)
		}
		if length == 0 {
			data := r.pending()
			safe := min(len(data), len(p)-n)
			for offset := 1; offset < safe; offset++ {
				b := data[offset]
				if r.echo.starts(b) || r.rules != nil && r.rules.first[b] {
					safe = offset
					break
				}
			}
			copy(p[n:], data[:safe])
			n += safe
			r.consume(safe)
			continue
		}
		wait, err := r.checkResponseOverlap(length, isMint)
		if err != nil {
			return n, r.finish(err)
		}
		if wait {
			if n > 0 {
				return n, nil
			}
			if err := r.readMore(); err != nil {
				return 0, r.finish(err)
			}
			continue
		}
		sentinel := echo.sentinel
		if isMint {
			value := api.Secret(string(r.pending()[:length]))
			sentinel, err = r.register(r.ctx, value, mint.ruleID)
			if err != nil {
				if r.ctx.Err() != nil {
					return n, r.finish(r.ctx.Err())
				}
				return n, r.finish(errResponseRegistry)
			}
			if sentinel == value || len(sentinel) != length {
				return n, r.finish(errResponseRegistry)
			}
			// Publication belongs to the canonical registry. Pick up its shared
			// committed snapshot before any later source bytes are scanned.
			if err := r.refresh(); err != nil {
				return n, r.finish(err)
			}
			if published, ok := r.echo.exact(value); !ok || published != sentinel {
				return n, r.finish(errResponseRegistry)
			}
		}
		if r.ctx.Err() != nil {
			return n, r.finish(r.ctx.Err())
		}
		r.consume(length)
		r.replacement = sentinel
		r.replaced = 0
	}
	return n, nil
}
