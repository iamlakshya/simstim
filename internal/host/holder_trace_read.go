package host

import (
	"context"
	"encoding/json"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/diag"
	"github.com/iamlakshya/simstim/internal/proto"
)

func (h *Holder) readOriginalTraceV1(raw json.RawMessage) (diag.TraceChunkV1, *proto.Error) {
	var args api.DebugTraceReadArgsV1
	if !api.ExactJSONFields(raw, args) || json.Unmarshal(raw, &args) != nil || args.ID != h.id || args.Offset < 0 || len(args.Epoch) > 64 {
		return diag.TraceChunkV1{}, badDebugArgs()
	}
	trace := h.dbg.tr.Load()
	if trace == nil || trace.f == nil {
		return diag.TraceChunkV1{}, &proto.Error{Code: "debug_off", Message: "this original holder has no active trace", Hint: "Start attach debug before collecting the trace."}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var chunk diag.TraceChunkV1
	var err error
	if args.ClockOnly {
		if args.Offset != 0 || args.Epoch != "" {
			return diag.TraceChunkV1{}, badDebugArgs()
		}
		chunk, err = trace.f.ReadClockV1(ctx)
	} else {
		chunk, err = trace.f.ReadCurrentV1(ctx, args.Epoch, args.Offset)
	}
	if err != nil {
		return diag.TraceChunkV1{}, &proto.Error{Code: "trace_changed", Message: err.Error(), Hint: "Collect the current trace again while its original holder is running."}
	}
	return chunk, nil
}
