package egress

import "golang.org/x/net/http2"

// Compact only when append needs room. Consumed opaque requests are cleared
// immediately, so neither a completed body nor its done channel is retained.
type physicalH2FrameQueue struct {
	frames []http2.FrameWriteRequest
	first  int
}

func (q *physicalH2FrameQueue) empty() bool                           { return q.first == len(q.frames) }
func (q *physicalH2FrameQueue) front() http2.FrameWriteRequest        { return q.frames[q.first] }
func (q *physicalH2FrameQueue) replace(frame http2.FrameWriteRequest) { q.frames[q.first] = frame }

func (q *physicalH2FrameQueue) push(frame http2.FrameWriteRequest) {
	if len(q.frames) == cap(q.frames) && q.first != 0 {
		count := copy(q.frames, q.frames[q.first:])
		clear(q.frames[count:])
		q.frames, q.first = q.frames[:count], 0
	}
	q.frames = append(q.frames, frame)
}

func (q *physicalH2FrameQueue) pop() http2.FrameWriteRequest {
	frame := q.frames[q.first]
	q.frames[q.first] = http2.FrameWriteRequest{}
	q.first++
	if q.empty() {
		q.frames, q.first = q.frames[:0], 0
	}
	return frame
}

func (q *physicalH2FrameQueue) clear() {
	clear(q.frames)
	q.frames, q.first = q.frames[:0], 0
}
