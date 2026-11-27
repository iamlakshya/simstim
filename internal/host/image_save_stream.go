package host

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"sync/atomic"

	"github.com/iamlakshya/simstim/internal/image"
)

// The existing guardian control pipe carries a length-delimited tar before
// its final ACK. No unscanned root bytes or private scan plan are spooled.
type savedRootChunkWriter struct {
	output io.Writer
	sent   int64
}

func (writer *savedRootChunkWriter) Write(data []byte) (int, error) {
	if int64(len(data)) > image.SavedRootInputMax-writer.sent {
		return 0, imageStreamFailure()
	}
	written := 0
	for len(data) != 0 {
		length := min(len(data), image.MaxChunk)
		if err := binary.Write(writer.output, binary.LittleEndian, uint32(length)); err != nil {
			return written, err
		}
		count, err := writer.output.Write(data[:length])
		written += count
		writer.sent += int64(count)
		if err != nil {
			return written, err
		}
		if count != length {
			return written, io.ErrShortWrite
		}
		data = data[length:]
	}
	return written, nil
}

type savedRootChunkReader struct {
	input     io.Reader
	remaining uint32
	received  int64
	ended     bool
}

func (reader *savedRootChunkReader) Read(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	if reader.ended {
		return 0, io.EOF
	}
	if reader.remaining == 0 {
		if err := binary.Read(reader.input, binary.LittleEndian, &reader.remaining); err != nil {
			return 0, err
		}
		if reader.remaining == 0 {
			reader.ended = true
			return 0, io.EOF
		}
		if reader.remaining > image.MaxChunk || int64(reader.remaining) > image.SavedRootInputMax-reader.received {
			return 0, imageStreamFailure()
		}
	}
	length := min(len(data), int(reader.remaining))
	count, err := io.ReadFull(reader.input, data[:length])
	reader.remaining -= uint32(count)
	reader.received += int64(count)
	return count, err
}

func imageStreamFailure() error {
	return UserError("image_too_large", "the saved-root stream exceeds its closed chunk or total bound", "Use a smaller root and the current host helper.")
}

func sendSavedRootStream(ctx context.Context, cancel context.CancelFunc, input io.WriteCloser, frames <-chan imageFrameResult, source func(context.Context, io.Writer) error) (*imageFrameResult, error) {
	done := make(chan error, 1)
	var produced atomic.Bool
	go func() {
		writer := &savedRootChunkWriter{output: input}
		err := source(ctx, writer)
		if err == nil {
			produced.Store(true)
			err = binary.Write(input, binary.LittleEndian, uint32(0))
		}
		done <- err
	}()
	select {
	case err := <-done:
		return nil, err
	case result, open := <-frames:
		if open && produced.Load() {
			if err := <-done; err != nil {
				return nil, err
			}
			return &result, nil
		}
		cancel()
		_ = input.Close()
		<-done // actual producer read/write and its stream cancellation join
		if !open {
			return nil, fmt.Errorf("the saved-root guardian ended before its source. Try: check host doctor")
		}
		if result.err == nil && result.frame.Phase == "review" {
			return nil, UserError("image_protocol", "the converter reported a review before its original source ended", "Use the current helper's owned image-save path.")
		}
		return &result, nil
	case <-ctx.Done():
		cancel()
		_ = input.Close()
		<-done
		return nil, ctx.Err()
	}
}
