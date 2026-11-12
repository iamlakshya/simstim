package link

// buffer holds the bytes of one direction of a stream, in one ring that
// grows by doubling up to the stream's window. Bytes are copied in, so the
// memory of a stream never passes its window, whatever the sizes of the
// frames or the writes that brought them. Under s.mu.
type buffer struct {
	b    []byte
	r, n int // where the bytes start in b, and how many there are
}

// minBuffer is the first size of a buffer that gets bytes.
const minBuffer = 4 << 10

func (q *buffer) len() int { return q.n }

func (q *buffer) cap() int { return len(q.b) }

// write copies p in. The caller has checked that the buffer then holds at
// most limit bytes.
func (q *buffer) write(p []byte, limit int) {
	if len(p) == 0 {
		return
	}
	if need := q.n + len(p); need > len(q.b) {
		size := max(min(minBuffer, limit), 2*len(q.b))
		for size < need {
			size *= 2
		}
		b := make([]byte, min(size, limit))
		q.copyTo(b)
		q.b, q.r = b, 0
	}
	w := (q.r + q.n) % len(q.b)
	c := copy(q.b[w:], p)
	copy(q.b, p[c:])
	q.n += len(p)
}

// copyTo copies the bytes in order to the start of b.
func (q *buffer) copyTo(b []byte) {
	if q.n == 0 {
		return
	}
	end := min(q.r+q.n, len(q.b))
	c := copy(b, q.b[q.r:end])
	copy(b[c:], q.b[:q.n-c])
}

// read moves up to len(p) bytes into p.
func (q *buffer) read(p []byte) int {
	n := min(len(p), q.n)
	if n == 0 {
		return 0
	}
	end := min(q.r+n, len(q.b))
	c := copy(p, q.b[q.r:end])
	copy(p[c:n], q.b)
	q.r = (q.r + n) % len(q.b)
	q.n -= n
	return n
}

// appendTo moves the first n bytes onto dst.
func (q *buffer) appendTo(dst []byte, n int) []byte {
	end := min(q.r+n, len(q.b))
	dst = append(dst, q.b[q.r:end]...)
	dst = append(dst, q.b[:n-(end-q.r)]...)
	q.r = (q.r + n) % len(q.b)
	q.n -= n
	return dst
}

// reset drops the bytes and the memory.
func (q *buffer) reset() { *q = buffer{} }

// waitq wakes every goroutine that waits for a change of state. A waiter
// takes the channel under s.mu, then waits on it without the lock. wake
// closes it. Under s.mu.
type waitq struct{ ch chan struct{} }

func (q *waitq) wait() <-chan struct{} {
	if q.ch == nil {
		q.ch = make(chan struct{})
	}
	return q.ch
}

func (q *waitq) wake() {
	if q.ch != nil {
		close(q.ch)
		q.ch = nil
	}
}
