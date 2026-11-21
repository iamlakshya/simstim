package diag

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"strconv"
)

// TraceReadMaxV1 bounds one read of the actual open writer's trace.
const TraceReadMaxV1 = 1 << 20

// TraceChunkV1 is diagnostic data, never a file or runtime capability.
type TraceChunkV1 struct {
	Epoch string `json:"epoch"`
	Data  []byte `json:"data"`
	Next  int64  `json:"next"`
	EOF   bool   `json:"eof"`
	Now   int64  `json:"now"`
}

type traceReadResultV1 struct {
	chunk TraceChunkV1
	err   error
}
type traceReadRequestV1 struct {
	epoch  string
	offset int64
	clock  bool
	reply  chan traceReadResultV1
}

func newTraceEpochV1() string { return rand.Text() }

// A read joins only the already queued prefix. A producer that keeps logging
// cannot turn one accepted bounded read into an unending drain.
func (t *File) drainReadPrefixV1(w *writer) {
	for remaining := len(t.q); remaining > 0; remaining-- {
		select {
		case record := <-t.q:
			w.add(record)
		default:
			remaining = 0
		}
	}
	if count := t.dropped.Swap(0); count > 0 {
		w.add(Record{T: Now(), Cat: CatAttach, Ev: EvDropped, Dropped: count})
	}
	w.flush()
}

// ReadCurrentV1 asks the original writer goroutine to flush and read its SAME
// open file. A replaced pathname cannot select another source. Turnover or a
// new writer invalidates an earlier epoch; no old/new files are concatenated.
func (t *File) ReadCurrentV1(ctx context.Context, epoch string, offset int64) (TraceChunkV1, error) {
	if t == nil || ctx == nil || offset < 0 || len(epoch) > 64 || offset > 0 && epoch == "" {
		return TraceChunkV1{}, fmt.Errorf("the trace read has no valid original cursor")
	}
	request := traceReadRequestV1{epoch: epoch, offset: offset, reply: make(chan traceReadResultV1, 1)}
	return t.readOriginalV1(ctx, request)
}

// ReadClockV1 samples this original writer's clock without copying a trace
// payload. The actual RPC bracket bounds cross-kernel clock uncertainty.
func (t *File) ReadClockV1(ctx context.Context) (TraceChunkV1, error) {
	if t == nil || ctx == nil {
		return TraceChunkV1{}, fmt.Errorf("the clock has no original trace writer")
	}
	return t.readOriginalV1(ctx, traceReadRequestV1{clock: true, reply: make(chan traceReadResultV1, 1)})
}

func (t *File) readOriginalV1(ctx context.Context, request traceReadRequestV1) (TraceChunkV1, error) {
	if t.reads == nil || t.done == nil || t.id == "" {
		return TraceChunkV1{}, fmt.Errorf("the trace has no original writer queue")
	}
	if err := ctx.Err(); err != nil {
		return TraceChunkV1{}, err
	}
	select {
	case <-ctx.Done():
		return TraceChunkV1{}, ctx.Err()
	case <-t.done:
		return TraceChunkV1{}, fmt.Errorf("the original trace writer has ended")
	case t.reads <- request:
	}
	// Once accepted, join this bounded read even if the request is cancelled.
	// The writer remains the sole owner of its descriptor and turnover.
	result := <-request.reply
	if err := ctx.Err(); err != nil {
		return TraceChunkV1{}, err
	}
	return result.chunk, result.err
}

func (w *writer) readTraceV1(id string, request traceReadRequestV1) traceReadResultV1 {
	w.flush()
	epoch := id + "." + strconv.FormatUint(w.epoch, 10)
	if w.err != nil || w.f == nil {
		return traceReadResultV1{err: fmt.Errorf("the original trace writer cannot be read: %v", w.err)}
	}
	if request.clock {
		return traceReadResultV1{chunk: TraceChunkV1{Epoch: epoch, Now: Now()}}
	}
	if request.epoch != "" && request.epoch != epoch || request.offset > w.size {
		return traceReadResultV1{err: fmt.Errorf("the original trace cursor expired during turnover")}
	}
	size := min(int64(TraceReadMaxV1), w.size-request.offset)
	data := make([]byte, int(size))
	count, err := w.f.ReadAt(data, request.offset)
	if err != nil && err != io.EOF {
		return traceReadResultV1{err: fmt.Errorf("read the actual trace writer: %w", err)}
	}
	next := request.offset + int64(count)
	return traceReadResultV1{chunk: TraceChunkV1{Epoch: epoch, Data: data[:count], Next: next, EOF: next == w.size, Now: Now()}}
}
