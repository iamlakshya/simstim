package egress

import (
	"bytes"
	"io"

	"golang.org/x/net/http2"
)

type physicalH2Output struct {
	header       [9]byte
	headerN      int
	reader       bytes.Reader
	framer       *http2.Framer
	frame        http2.FrameHeader
	remaining    uint32
	offset       uint32
	pad          uint32
	padKnown     bool
	control      []byte
	headerEnding uint32
	headerOpen   uint32
	discard      bool
}

func (o *physicalH2Output) init() {
	o.framer = http2.NewFramer(io.Discard, &o.reader)
	o.framer.SetMaxReadFrameSize((1 << 24) - 1) // peer may raise the original outgoing frame size; DATA stays streamed
}

func (c *physicalH2Conn) Write(payload []byte) (accepted int, writeErr error) {
	c.owner.mu.Lock()
	if c.owner.closed {
		c.owner.mu.Unlock()
		return 0, io.ErrClosedPipe
	}
	c.writes.Add(1)
	c.owner.mu.Unlock()
	defer c.writes.Done()
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	defer func() {
		// A failed prefix cannot be resumed as another physical frame. Cut
		// this original writer before unlocking it or returning the failure;
		// connection settlement still joins every admitted native write.
		if writeErr != nil {
			_ = c.Close()
		}
	}()
	o := &c.output
	for len(payload) != 0 {
		if err := c.ctx.Err(); err != nil {
			return accepted, err
		}
		if o.headerN < len(o.header) {
			count := copy(o.header[o.headerN:], payload)
			o.headerN += count
			accepted += count
			payload = payload[count:]
			if o.headerN != len(o.header) {
				continue
			}
			o.reader.Reset(o.header[:])
			frame, err := o.framer.ReadFrameHeader()
			if err != nil {
				return accepted, err
			}
			o.frame, o.remaining, o.offset = frame, frame.Length, 0
			o.pad, o.padKnown, o.discard = 0, false, false
			if frame.Type == http2.FrameData {
				c.owner.mu.Lock()
				stream := c.owner.streams[frame.StreamID]
				known := stream != nil && !stream.finished
				if known {
					o.discard = c.owner.scheduler && stream.reset
				}
				c.owner.mu.Unlock()
				if !known {
					return accepted, errPhysicalH2Binding
				}
			}
			if frame.Type == http2.FrameSettings || frame.Type == http2.FramePing || frame.Type == http2.FrameRSTStream {
				o.control = append(o.control[:0], o.header[:]...)
			} else if !o.discard {
				if err := c.writePhysicalH2(o.header[:]); err != nil {
					return accepted, err
				}
			}
		}
		if o.remaining != 0 {
			count := min(len(payload), int(o.remaining))
			part := payload[:count]
			if o.control != nil {
				o.control = append(o.control, part...)
				accepted += count
				o.offset += uint32(count)
				o.remaining -= uint32(count)
				payload = payload[count:]
			} else if o.discard {
				accepted += count
				o.offset += uint32(count)
				o.remaining -= uint32(count)
				payload = payload[count:]
			} else {
				if o.frame.Type == http2.FrameData && o.frame.Flags.Has(http2.FlagDataPadded) && !o.padKnown {
					o.pad, o.padKnown = uint32(part[0]), true
					if o.pad+1 > o.frame.Length {
						return accepted, errPhysicalH2Binding
					}
				}
				n, err := c.plainWrite(part)
				if n > 0 && o.frame.Type == http2.FrameData {
					start, end := uint32(0), o.frame.Length
					if o.frame.Flags.Has(http2.FlagDataPadded) {
						start, end = 1, end-o.pad
					}
					from, to := max(start, o.offset), min(end, o.offset+uint32(n))
					if from < to {
						if observeErr := c.owner.physicalData(o.frame.StreamID, part[from-o.offset:to-o.offset]); observeErr != nil {
							return accepted + n, observeErr
						}
					}
				}
				accepted += n
				o.offset += uint32(n)
				o.remaining -= uint32(n)
				payload = payload[n:]
				if err != nil {
					return accepted, err
				}
				if n != count {
					return accepted, io.ErrShortWrite
				}
			}
		}
		if o.remaining == 0 {
			if o.control != nil {
				if err := c.finishPhysicalH2Control(); err != nil {
					return accepted, err
				}
			} else {
				if o.discard {
					c.refundFlow(o.frame.Length)
				}
				c.finishPhysicalH2Frame()
			}
			o.headerN, o.offset = 0, 0
			o.control = nil
		}
	}
	return accepted, nil
}

func (c *physicalH2Conn) writePhysicalH2(payload []byte) error {
	for len(payload) != 0 {
		count, err := c.plainWrite(payload)
		payload = payload[count:]
		if err != nil {
			return err
		}
		if count == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func (c *physicalH2Conn) finishPhysicalH2Control() error {
	o := &c.output
	o.reader.Reset(o.control[9:])
	frame, err := o.framer.ReadFrameForHeader(o.frame)
	if err != nil {
		return err
	}
	raw := o.control
	switch frame := frame.(type) {
	case *http2.PingFrame:
		if frame.Flags.Has(http2.FlagPingAck) {
			c.owner.mu.Lock()
			id, private := c.owner.fences[frame.Data]
			var finish func()
			if private {
				delete(c.owner.fences, frame.Data)
				if stream := c.owner.streams[id]; stream != nil {
					stream.settled = true
					finish = c.owner.finishLocked(stream)
				}
				c.owner.notifyLocked()
			}
			c.owner.mu.Unlock()
			if private {
				// The single ordered writer reached this private ACK only
				// after every preceding native Write returned and every
				// successful DATA prefix was observed. Never expose this ACK.
				if finish != nil {
					go finish()
				}
				return nil
			}
		}
	case *http2.SettingsFrame:
		if !frame.IsAck() {
			var settings []http2.Setting
			if err := frame.ForeachSetting(func(setting http2.Setting) error {
				if setting.ID == http2.SettingMaxHeaderListSize {
					setting.Val = physicalH2OriginalHeaderLimit
				}
				settings = append(settings, setting)
				return nil
			}); err != nil {
				return err
			}
			var encoded bytes.Buffer
			if err := http2.NewFramer(&encoded, nil).WriteSettings(settings...); err != nil {
				return err
			}
			raw = encoded.Bytes()
		}
	}
	if err := c.writePhysicalH2(raw); err != nil {
		return err
	}
	if ping, ok := frame.(*http2.PingFrame); ok && ping.Flags.Has(http2.FlagPingAck) {
		c.owner.mu.Lock()
		if count := c.owner.guestPings[ping.Data]; count > 0 {
			if count == 1 {
				delete(c.owner.guestPings, ping.Data)
			} else {
				c.owner.guestPings[ping.Data]--
			}
			c.owner.guestPingN--
			c.owner.notifyLocked()
		}
		c.owner.mu.Unlock()
	}
	c.finishPhysicalH2Frame()
	return nil
}

func (c *physicalH2Conn) finishPhysicalH2Frame() {
	o := &c.output
	id, ended, reset, selectedDone := o.frame.StreamID, false, false, false
	switch o.frame.Type {
	case http2.FrameData:
		ended, selectedDone = o.frame.Flags.Has(http2.FlagDataEndStream), true
	case http2.FrameHeaders:
		if o.frame.Flags.Has(http2.FlagHeadersEndStream) {
			o.headerEnding = id
		}
		if o.frame.Flags.Has(http2.FlagHeadersEndHeaders) {
			ended, o.headerEnding, o.headerOpen, selectedDone = o.headerEnding == id, 0, 0, true
		} else {
			o.headerOpen = id
		}
	case http2.FrameContinuation:
		if o.frame.Flags.Has(http2.FlagContinuationEndHeaders) {
			ended, o.headerEnding, o.headerOpen, selectedDone = o.headerEnding == id, 0, 0, true
		}
	case http2.FrameRSTStream:
		ended, reset, selectedDone = true, true, true
	case http2.FrameWindowUpdate:
		// The original server charges stream receive-window updates to
		// that stream's scheduler too. Their native completion must retire
		// the selection before a later policy cancellation can settle it.
		selectedDone = true
	}
	c.owner.mu.Lock()
	var finish func()
	if stream := c.owner.streams[id]; stream != nil {
		if c.owner.scheduler && selectedDone && stream.selected > 0 {
			stream.selected--
		}
		if ended && (!stream.reset || reset) {
			stream.reset, stream.settled = reset, true
			if reset {
				stream.nativeReset = false
			}
		}
		finish = c.owner.finishLocked(stream)
	}
	if c.owner.writerWaiters > 0 {
		c.owner.notifyLocked()
	}
	c.owner.mu.Unlock()
	if finish != nil {
		go finish()
	}
}
