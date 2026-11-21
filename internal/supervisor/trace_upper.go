package supervisor

import (
	"encoding/json"
	"math"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/diag"
	"github.com/iamlakshya/simstim/internal/proto"
)

const traceUpperLimitV1 = 2048

func (s *Supervisor) traceUpperAttachV1(c *client, args proto.Attach) {
	c.traceUpperOn.Store(false)
	valid := args.Trace != "" && len(args.Trace) <= 64
	for _, char := range args.Trace {
		if char != '.' && char != '-' && char != '_' && (char < '0' || char > '9') && (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') {
			valid = false
		}
	}
	if len(args.Categories) > 0 {
		attach := false
		for _, category := range args.Categories {
			if category == diag.CatAttach {
				attach = true
			}
		}
		valid = valid && attach
	}
	s.traceUpperMu.Lock()
	c.traceUpperName, c.traceUpperKey, c.traceUpperBridge, c.traceUpperPending = "", 0, 0, nil
	if valid {
		c.traceUpperName = args.Trace
	}
	s.traceUpperMu.Unlock()
	c.traceUpperOn.Store(valid)
}

func (s *Supervisor) traceUpperBridgeV1(c *client, data []byte) {
	if !c.traceUpperOn.Load() {
		return
	}
	received := diag.Now()
	bridge := proto.DecodeTime(data)
	s.traceUpperMu.Lock()
	defer s.traceUpperMu.Unlock()
	if len(data) != 8 || bridge <= 0 || bridge > received {
		s.traceUpperDropped = true
		c.traceUpperBridge = 0
		return
	}
	c.traceUpperBridge, c.traceUpperReceived = bridge, received
}

func (s *Supervisor) traceUpperInputV1(c *client, size int) {
	if size == 0 || !c.traceUpperOn.Load() {
		return
	}
	s.traceUpperMu.Lock()
	defer s.traceUpperMu.Unlock()
	if c.traceUpperName == "" || c.traceUpperKey == math.MaxInt64 {
		s.traceUpperDropped = true
		return
	}
	c.traceUpperKey++
	if c.traceUpperBridge <= 0 {
		s.traceUpperDropped = true
		c.traceUpperPending = nil
		return
	}
	row := &api.TraceUpperSampleV1{Trace: c.traceUpperName, Key: c.traceUpperKey, Bridge: c.traceUpperBridge, Received: c.traceUpperReceived}
	c.traceUpperBridge = 0
	c.traceUpperPending = row
	// A partial input can be followed by another input frame before the
	// controlled fixture answers. Only an observed output consumes a ring
	// entry; split output frames update that SAME entry through the window.
}

func (s *Supervisor) appendTraceUpperV1(row *api.TraceUpperSampleV1) {
	if len(s.traceUpper) == traceUpperLimitV1 {
		copy(s.traceUpper, s.traceUpper[1:])
		s.traceUpper[len(s.traceUpper)-1] = row
		s.traceUpperDropped = true
	} else {
		s.traceUpper = append(s.traceUpper, row)
	}
}

func (s *Supervisor) traceUpperOutputV1(c *client) {
	if !c.traceUpperOn.Load() {
		return
	}
	ended := diag.Now()
	s.traceUpperMu.Lock()
	if row := c.traceUpperPending; row != nil {
		if row.Frames == 0 {
			s.appendTraceUpperV1(row)
		}
		row.End = ended
		row.Frames++
	}
	s.traceUpperMu.Unlock()
}

func (s *Supervisor) traceUpperSnapshotV1() api.TraceUpperSnapshotV1 {
	s.mu.Lock()
	id, generation := s.id, s.generation
	s.mu.Unlock()
	s.traceUpperMu.Lock()
	defer s.traceUpperMu.Unlock()
	result := api.TraceUpperSnapshotV1{Version: 1, Session: id, Generation: generation, Dropped: s.traceUpperDropped, Samples: make([]api.TraceUpperSampleV1, 0, len(s.traceUpper))}
	for _, row := range s.traceUpper {
		result.Samples = append(result.Samples, *row)
	}
	return result
}

func (s *Supervisor) traceUpperRequestV1(c *client, request proto.Request) bool {
	if request.Op != api.OpDebugTraceUpperV1 {
		return false
	}
	var args struct {
		ID string `json:"id"`
	}
	if !api.ExactJSONFields(request.Args, args) || json.Unmarshal(request.Args, &args) != nil || args.ID != s.id {
		c.reply(request.ID, nil, &proto.Error{Code: "bad_request", Message: "the trace snapshot does not name this original supervisor", Hint: "Collect the current session through its full address."})
		return true
	}
	c.reply(request.ID, s.traceUpperSnapshotV1(), nil)
	return true
}
