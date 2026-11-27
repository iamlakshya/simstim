package egress

import (
	"bytes"
	"errors"
	"io"
	"math"
	"net"
	"time"

	"golang.org/x/net/http2"
)

var errPhysicalH2ReadInterrupt = errors.New("private HTTP reader wake")

// Each existing row can enqueue its own cancellation once. No new reader or
// cancellation goroutine is created. The native read deadline wakes the same
// TLS reader; its partial frame remains in the original bounded raw buffer.
func (c *physicalH2Conn) requestReset(id uint32) {
	if c.ctx.Err() != nil {
		return
	}
	c.controlMu.Lock()
	if len(c.controls) >= 2*maxActiveFlows {
		c.controlMu.Unlock()
		_ = c.Close()
		return
	}
	c.controls = append(c.controls, id)
	_ = c.actual.SetReadDeadline(time.Now())
	c.controlMu.Unlock()
}

// Called only between complete header blocks exposed to the original server.
// An original frame partly read from TLS can remain buffered behind this RST.
// A HEADERS/CONTINUATION block already exposed to the server always finishes
// first; private cancellation never creates an invalid interleaving.
func (c *physicalH2Conn) nextReset() []byte {
	c.controlMu.Lock()
	if len(c.controls) == 0 && c.credit == 0 {
		c.controlMu.Unlock()
		return nil
	}
	var id uint32
	if len(c.controls) != 0 {
		id = c.controls[0]
		copy(c.controls, c.controls[1:])
		c.controls[len(c.controls)-1] = 0
		c.controls = c.controls[:len(c.controls)-1]
	}
	credit := uint32(min(c.credit, uint64(math.MaxInt32)))
	c.credit -= uint64(credit)
	deadline := c.readDeadline
	if len(c.controls) != 0 || c.credit != 0 {
		deadline = time.Now()
	}
	_ = c.actual.SetReadDeadline(deadline)
	c.controlMu.Unlock()
	var encoded bytes.Buffer
	writer := http2.NewFramer(&encoded, nil)
	if id != 0 {
		_ = writer.WriteRSTStream(id, http2.ErrCodeCancel)
	}
	if credit != 0 {
		_ = writer.WriteWindowUpdate(0, credit)
	}
	return encoded.Bytes()
}

func (c *physicalH2Conn) refillPhysicalFrame(size int) error {
	var buffer [16 << 10]byte
	for c.inRaw.Len() < size {
		count, err := c.actual.Read(buffer[:min(len(buffer), size-c.inRaw.Len())])
		if count > 0 {
			_, _ = c.inRaw.Write(buffer[:count])
		}
		if err != nil {
			var timeout net.Error
			if errors.As(err, &timeout) && timeout.Timeout() {
				c.controlMu.Lock()
				// A private wake may shorten a live caller deadline, but it
				// must never swallow that deadline once it really expires.
				// During an exposed CONTINUATION the queued control cannot
				// drain, so mistaking expiry for a wake would spin forever.
				liveDeadline := c.readDeadline.IsZero() || time.Now().Before(c.readDeadline)
				interrupted := (len(c.controls) != 0 || c.credit != 0) && liveDeadline
				if interrupted {
					_ = c.actual.SetReadDeadline(c.readDeadline)
				}
				c.controlMu.Unlock()
				if interrupted {
					return errPhysicalH2ReadInterrupt
				}
			}
			return err
		}
		if count == 0 {
			return io.ErrNoProgress
		}
	}
	return nil
}

func (c *physicalH2Conn) readPhysicalFrame() (http2.Frame, error) {
	if c.frameReady {
		c.inRaw.Reset()
		c.frameReady = false
	}
	if err := c.refillPhysicalFrame(9); err != nil {
		return nil, err
	}
	c.inHeaderReader.Reset(c.inRaw.Bytes()[:9])
	header, err := c.inHeaderFrames.ReadFrameHeader()
	if err != nil {
		return nil, err
	}
	if header.Length > physicalH2OriginalFrameLimit {
		return nil, http2.ErrFrameTooLarge
	}
	if err := c.refillPhysicalFrame(9 + int(header.Length)); err != nil {
		return nil, err
	}
	c.inReader.Reset(c.inRaw.Bytes())
	return c.inFrames.ReadFrame()
}

// This is the one original native writer, serialised with ordinary frames.
// Scheduler retirement and the selected-frame count establish that no later
// target DATA can appear. The actual RST is written only at a frame and header
// block boundary, before the original Source and worker can be released.
func (c *physicalH2Conn) writeReset(id uint32) error {
	var encoded bytes.Buffer
	if err := http2.NewFramer(&encoded, nil).WriteRSTStream(id, http2.ErrCodeCancel); err != nil {
		return err
	}
	for {
		c.writeMu.Lock()
		if err := c.ctx.Err(); err != nil {
			c.writeMu.Unlock()
			return err
		}
		if c.output.headerN == 0 && c.output.headerOpen == 0 {
			err := c.writePhysicalH2(encoded.Bytes())
			c.writeMu.Unlock()
			if err != nil {
				_ = c.Close()
			}
			return err
		}
		c.owner.mu.Lock()
		c.owner.writerWaiters++
		changed := c.owner.changed
		c.owner.mu.Unlock()
		c.writeMu.Unlock()
		select {
		case <-changed:
		case <-c.ctx.Done():
		}
		c.owner.mu.Lock()
		c.owner.writerWaiters--
		c.owner.mu.Unlock()
	}
}

// DATA discarded before native frame admission must not spend connection
// flow credit. Accumulate its exact DATA payload length, including any padding,
// and return it to the original server through a connection WINDOW_UPDATE.
// That input generates no ACK and consumes no outgoing control-queue slot.
func (c *physicalH2Conn) refundFlow(size uint32) {
	if size == 0 || c.ctx.Err() != nil {
		return
	}
	c.controlMu.Lock()
	if uint64(size) > math.MaxUint64-c.credit {
		c.controlMu.Unlock()
		_ = c.Close()
		return
	}
	c.credit += uint64(size)
	_ = c.actual.SetReadDeadline(time.Now())
	c.controlMu.Unlock()
}
