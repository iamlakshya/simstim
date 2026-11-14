package egress

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
)

const requestSecretStreamChunk = 32 << 10

type requestSecretStreamGetter func(context.Context) (*requestSecretSnapshot, error)

var (
	errRequestSecretStreamRead   = errors.New("request payload stream failed. Try: repeat the allowed request")
	errRequestSecretStreamView   = errors.New("request stand-in view is unavailable. Try: resume the session before retrying")
	errRequestSecretStreamMisuse = errors.New("a credential stand-in cannot be sent in a WebSocket payload. Try: use the session's bound credential in its named HTTPS header")
	errRequestSecretStreamBounds = errors.New("request stand-in carry exceeds its bound. Try: resume the session before retrying")
)

// Only raw public stand-ins are inspected. This reader adds no URL/header
// projection, binding exemption, real value, network grant or replacement.
// The caller binds get to its exact canonical request scope/generation.
type requestSecretStreamReader struct{ state *requestSecretStreamState }

type requestSecretStreamRange struct{ start, end int }

type requestSecretStreamState struct {
	mu               *sync.Mutex
	ctx              context.Context
	cancel           context.CancelFunc
	input            io.ReadCloser
	get              requestSecretStreamGetter
	view             *requestSecretSnapshot
	buckets          map[byte]requestSecretStreamRange
	first            [4]uint64
	maxLength        int
	buffer           []byte
	start            int
	scratch          [requestSecretStreamChunk]byte
	active           bool
	low, high, depth int
	eof              bool
	emptyReads       int
	terminal         error
	closeDone        chan struct{}
	closeErr         error
}

func (requestSecretStreamReader) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, api.Redacted)
}
func (*requestSecretStreamState) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, api.Redacted)
}

// Constructor failure leaves input with its caller. Success owns input.Close.
// Close must unblock an active Read promptly and must not join this reader.
// No read goroutine or whole-message buffer is created.
func newRequestSecretStreamReader(ctx context.Context, input io.ReadCloser, get requestSecretStreamGetter) (*requestSecretStreamReader, error) {
	if ctx == nil || input == nil || get == nil {
		return nil, errors.New("request payload tripwire is not configured. Try: update matching helpers before upgrading")
	}
	if ctx.Err() != nil {
		return nil, errRequestSecretStreamRead
	}
	owned, cancel := context.WithCancel(ctx)
	s := &requestSecretStreamState{mu: new(sync.Mutex), ctx: owned, cancel: cancel, input: input, get: get, closeDone: make(chan struct{})}
	context.AfterFunc(owned, func() { s.closeErr = input.Close(); close(s.closeDone) })
	return &requestSecretStreamReader{state: s}, nil
}

func (r *requestSecretStreamReader) Close() error {
	s := r.state
	s.cancel()
	<-s.closeDone
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clear()
	if s.terminal == nil {
		s.terminal = io.ErrClosedPipe
	}
	if s.closeErr != nil {
		return errRequestSecretStreamRead
	}
	return nil
}

func (s *requestSecretStreamState) clear() {
	clear(s.buffer)
	s.buffer = nil
	s.start = 0
	clear(s.scratch[:])
	s.view = nil
	s.get = nil
	s.input = nil
	s.buckets = nil
	s.first = [4]uint64{}
	s.active = false
}

func (s *requestSecretStreamState) finish(err error) error {
	s.cancel()
	<-s.closeDone
	if errors.Is(err, io.EOF) && s.closeErr != nil {
		err = errRequestSecretStreamRead
	}
	s.clear()
	s.terminal = err
	return err
}

// Snapshot/public union ownership stays with the sole canonical registry.
// Only index offsets and a first-byte mask are derived. No value is cloned.
func (s *requestSecretStreamState) refresh() (bool, error) {
	if s.ctx.Err() != nil {
		return false, errRequestSecretStreamRead
	}
	view, err := s.get(s.ctx)
	if err != nil || view == nil {
		return false, errRequestSecretStreamView
	}
	if view == s.view {
		return false, nil
	}
	buckets := make(map[byte]requestSecretStreamRange)
	var first [4]uint64
	maximum := s.maxLength
	for i, value := range view.tripwires {
		if value == "" || strings.IndexByte(string(value), 0) >= 0 || i > 0 && view.tripwires[i-1] >= value {
			return false, errRequestSecretStreamView
		}
		maximum = max(maximum, len(value))
		b := value[0]
		bucket, exists := buckets[b]
		if !exists {
			bucket.start = i
		}
		bucket.end = i + 1
		buckets[b] = bucket
		first[b/64] |= uint64(1) << (b % 64)
	}
	if maximum > int(^uint(0)>>1)-requestSecretStreamChunk {
		return false, errRequestSecretStreamBounds
	}
	s.view, s.buckets, s.first, s.maxLength = view, buckets, first, maximum
	s.active = false
	return true, nil
}

func (s *requestSecretStreamState) starts(b byte) bool { return s.first[b/64]&(uint64(1)<<(b%64)) != 0 }
func (s *requestSecretStreamState) pending() []byte    { return s.buffer[s.start:] }

// The active range consists only of sorted stand-ins sharing the scanned
// prefix. Its depth is retained across input Reads, never across generations.
func (s *requestSecretStreamState) front() (matched, wait bool) {
	data := s.pending()
	if len(data) == 0 || !s.starts(data[0]) {
		return false, false
	}
	if !s.active {
		bucket := s.buckets[data[0]]
		s.low, s.high, s.depth, s.active = bucket.start, bucket.end, 1, true
	}
	values := s.view.tripwires
	for s.low < s.high {
		if len(values[s.low]) == s.depth {
			return true, false
		}
		if s.depth == len(data) {
			return false, !s.eof
		}
		b, depth := data[s.depth], s.depth
		base, count := s.low, s.high-s.low
		low := base + sort.Search(count, func(i int) bool { return values[base+i][depth] >= b })
		high := base + sort.Search(count, func(i int) bool { return values[base+i][depth] > b })
		if low == high {
			return false, false
		}
		s.low, s.high, s.depth = low, high, depth+1
	}
	return false, false
}

func (s *requestSecretStreamState) consume(length int) {
	clear(s.buffer[s.start : s.start+length])
	s.start += length
	s.active = false
	if s.start == len(s.buffer) {
		s.buffer = s.buffer[:0]
		s.start = 0
	}
}

func (s *requestSecretStreamState) readMore() error {
	changed, err := s.refresh()
	if err != nil {
		return err
	}
	if changed {
		return nil
	} // rescan pending originals before blocking for input
	remaining := len(s.pending())
	if s.start != 0 {
		copy(s.buffer, s.pending())
		clear(s.buffer[remaining:])
		s.buffer = s.buffer[:remaining]
		s.start = 0
	}
	budget := s.maxLength + requestSecretStreamChunk
	want := min(requestSecretStreamChunk, budget-remaining)
	if want <= 0 {
		return errRequestSecretStreamBounds
	}
	n, readErr := s.input.Read(s.scratch[:want])
	if n < 0 || n > want {
		return errRequestSecretStreamRead
	}
	if s.ctx.Err() != nil {
		clear(s.scratch[:n])
		return errRequestSecretStreamRead
	}
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		clear(s.scratch[:n])
		return errRequestSecretStreamRead
	}
	if n == 0 && readErr == nil {
		s.emptyReads++
		if s.emptyReads >= 100 {
			return errRequestSecretStreamRead
		}
		return nil
	}
	s.emptyReads = 0
	if n != 0 {
		length := remaining + n
		if length > cap(s.buffer) {
			capacity := min(budget, max(length, 2*cap(s.buffer)))
			next := make([]byte, length, capacity)
			copy(next, s.buffer)
			clear(s.buffer)
			s.buffer = next
		} else {
			s.buffer = s.buffer[:length]
		}
		copy(s.buffer[remaining:], s.scratch[:n])
		clear(s.scratch[:n])
	}
	if errors.Is(readErr, io.EOF) {
		s.eof = true
	}
	_, err = s.refresh()
	return err
}

func (r *requestSecretStreamReader) Read(output []byte) (int, error) {
	if len(output) == 0 {
		return 0, nil
	}
	s := r.state
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.terminal != nil {
		return 0, s.terminal
	}
	if _, err := s.refresh(); err != nil {
		return 0, s.finish(err)
	}
	written := 0
	for written < len(output) {
		if s.ctx.Err() != nil {
			return written, s.finish(errRequestSecretStreamRead)
		}
		data := s.pending()
		if len(data) == 0 {
			if written != 0 {
				return written, nil
			}
			if s.eof {
				return 0, s.finish(io.EOF)
			}
			if err := s.readMore(); err != nil {
				return 0, s.finish(err)
			}
			continue
		}
		matched, wait := s.front()
		if matched {
			return written, s.finish(errRequestSecretStreamMisuse)
		}
		if wait {
			if written != 0 {
				return written, nil
			}
			if err := s.readMore(); err != nil {
				return 0, s.finish(err)
			}
			continue
		}
		length := min(len(data), len(output)-written)
		for i := 1; i < length; i++ {
			if s.starts(data[i]) {
				length = i
				break
			}
		}
		copy(output[written:], data[:length])
		written += length
		s.consume(length)
	}
	return written, nil
}
