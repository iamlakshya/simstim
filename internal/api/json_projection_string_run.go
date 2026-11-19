package api

// stringByteRun consumes ordinary ASCII from the already-owned source window
// and returns the next escape, control, quote or non-ASCII byte. It allocates
// no buffer/cache and changes no grammar or source read policy.
func (c *jsonCursor) stringByteRun() (byte, error) {
	for {
		first, err := c.peek()
		if err != nil {
			return 0, err
		}
		if first < 0x20 || first == '"' || first == '\\' || first >= 0x80 {
			c.pos++
			return first, nil
		}
		var piece []byte
		var window *jsonProjectionWindow
		if c.p.r == nil {
			end := c.pos + min(int64(4096), c.p.size-c.pos)
			piece = c.p.raw[c.pos:end]
		} else {
			for i := range *c.p.windows {
				current := &c.p.windows[i]
				if current.n > 0 && c.pos >= current.base && c.pos < current.base+int64(current.n) {
					window = current
					piece = current.buffer[c.pos-current.base : current.n]
					break
				}
			}
			if window == nil {
				return 0, ErrJSONProjection
			}
		}
		for i := range piece {
			b := first
			if i != 0 {
				// The first byte used peek's check. Every following byte
				// keeps the original cancellation check, cursor position
				// and LRU clock/update order, including a partial failure.
				if err := c.p.ctx.Err(); err != nil {
					return 0, err
				}
				if window != nil {
					c.p.clock++
					window.used = c.p.clock
				}
				b = piece[i]
			}
			c.pos++
			if b < 0x20 || b == '"' || b == '\\' || b >= 0x80 {
				return b, nil
			}
		}
	}
}
