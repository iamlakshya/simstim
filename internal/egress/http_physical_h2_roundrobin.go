package egress

import (
	"math"

	"golang.org/x/net/http2"
)

// Admission order and a next index define deterministic rounds. The original
// server still bounds stream admission and control queueing. Opaque requests
// are consumed only when selected for its one original native writer.
type physicalH2RoundRobin struct {
	streams    map[uint32]*physicalH2FrameQueue
	order      []uint32
	next       int
	controls   physicalH2FrameQueue
	spare      []*physicalH2FrameQueue
	classifier http2.WriteScheduler
}

func newPhysicalH2RoundRobin() *physicalH2RoundRobin {
	return &physicalH2RoundRobin{
		streams:    make(map[uint32]*physicalH2FrameQueue, maxActiveFlows),
		order:      make([]uint32, 0, maxActiveFlows),
		classifier: http2.NewRandomWriteScheduler(),
	}
}

func (s *physicalH2RoundRobin) OpenStream(id uint32, _ http2.OpenStreamOptions) {
	if id == 0 || s.streams[id] != nil {
		panic(errPhysicalH2Binding)
	}
	var queue *physicalH2FrameQueue
	if count := len(s.spare); count != 0 {
		queue = s.spare[count-1]
		s.spare[count-1] = nil
		s.spare = s.spare[:count-1]
	} else {
		queue = new(physicalH2FrameQueue)
	}
	s.streams[id] = queue
	s.order = append(s.order, id)
}

func (s *physicalH2RoundRobin) CloseStream(id uint32) {
	queue := s.streams[id]
	if queue == nil {
		return
	}
	queue.clear()
	s.spare = append(s.spare, queue)
	delete(s.streams, id)
	for index, stream := range s.order {
		if stream != id {
			continue
		}
		copy(s.order[index:], s.order[index+1:])
		s.order[len(s.order)-1] = 0
		s.order = s.order[:len(s.order)-1]
		if index < s.next {
			s.next--
		}
		if s.next == len(s.order) {
			s.next = 0
		}
		return
	}
}

// The reference Go1.26 standard server ignores RFC7540 priorities. Newer
// RFC9218 updates are not exposed to custom schedulers by this pinned API.
func (*physicalH2RoundRobin) AdjustStream(uint32, http2.PriorityParam) {}

func (s *physicalH2RoundRobin) Push(frame http2.FrameWriteRequest) {
	if s.control(frame) {
		s.controls.push(frame)
		return
	}
	queue := s.streams[frame.StreamID()]
	if queue == nil {
		if frame.DataSize() != 0 {
			panic(errPhysicalH2Binding)
		}
		s.controls.push(frame)
		return
	}
	queue.push(frame)
}

// The public request has no frame-type accessor. On the exact pinned public
// constructor, closing a temporary stream discards its ordinary frame copy
// but retains a connection-control RST. There is at most one probe, never
// DATA consumption, native IO or a completion notification. Only the original
// request enters the real queue. Compatibility/native tests bind this rule.
func (s *physicalH2RoundRobin) control(frame http2.FrameWriteRequest) bool {
	id := frame.StreamID()
	if id == 0 {
		return true
	}
	if frame.DataSize() != 0 {
		return false
	}
	s.classifier.OpenStream(id, http2.OpenStreamOptions{})
	s.classifier.Push(frame)
	s.classifier.CloseStream(id)
	_, control := s.classifier.Pop()
	return control
}

func (s *physicalH2RoundRobin) Pop() (http2.FrameWriteRequest, bool) {
	if !s.controls.empty() {
		return s.controls.pop(), true
	}
	for offset := range len(s.order) {
		index := (s.next + offset) % len(s.order)
		queue := s.streams[s.order[index]]
		if queue.empty() {
			continue
		}
		frame, rest, count := queue.front().Consume(math.MaxInt32)
		switch count {
		case 0:
			continue
		case 1:
			queue.pop()
		case 2:
			queue.replace(rest)
		default:
			panic(errPhysicalH2Binding)
		}
		s.next = (index + 1) % len(s.order)
		return frame, true
	}
	return http2.FrameWriteRequest{}, false
}
