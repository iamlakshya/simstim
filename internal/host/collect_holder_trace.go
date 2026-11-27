package host

import (
	"encoding/json"
	"fmt"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/diag"
)

// collectOriginalHolderTraceV1 reads only the bounded current writer of the
// original authenticated holder. The guest's bytes remain diagnostic claims;
// no path, owner, native resource or process can be selected by its answer.
func collectOriginalHolderTraceV1(id string, files map[string]collected, budget *int64) []string {
	if budget == nil || *budget <= 0 {
		return []string{id + ": the bounded original trace budget is full"}
	}
	m, err := LoadManifest(id)
	if err != nil {
		return nil
	}
	var data []byte
	var brackets []map[string]int64
	epoch := ""
	for count := 0; count < 8; count++ {
		before := diag.Now()
		raw, err := HolderRequest(id, api.OpDebugTraceReadV1, api.DebugTraceReadArgsV1{ID: id, ClockOnly: true}, holderWait)
		after := diag.Now()
		var clock diag.TraceChunkV1
		if err != nil || json.Unmarshal(raw, &clock) != nil || clock.Now <= 0 || clock.Epoch == "" || len(clock.Epoch) > 64 || len(clock.Data) != 0 || epoch != "" && clock.Epoch != epoch {
			return []string{fmt.Sprintf("%s: no original guest clock calibration: %v", id, err)}
		}
		epoch = clock.Epoch
		brackets = append(brackets, map[string]int64{"host_before": before, "host_after": after, "guest_now": clock.Now})
	}
	var offset int64
	for {
		before := diag.Now()
		raw, err := HolderRequest(id, api.OpDebugTraceReadV1, api.DebugTraceReadArgsV1{ID: id, Epoch: epoch, Offset: offset}, holderWait)
		after := diag.Now()
		var chunk diag.TraceChunkV1
		if err != nil || json.Unmarshal(raw, &chunk) != nil || len(chunk.Data) > api.DebugReadMax || chunk.Now <= 0 || chunk.Epoch == "" || len(chunk.Epoch) > 64 || epoch != "" && chunk.Epoch != epoch || chunk.Next != offset+int64(len(chunk.Data)) || len(chunk.Data) == 0 && !chunk.EOF || len(data)+len(chunk.Data) > diag.DefaultMax || int64(len(data)+len(chunk.Data)) > *budget {
			return []string{fmt.Sprintf("%s: no complete original guest trace: %v", id, err)}
		}
		data = append(data, chunk.Data...)
		brackets = append(brackets, map[string]int64{"host_before": before, "host_after": after, "guest_now": chunk.Now})
		epoch, offset = chunk.Epoch, chunk.Next
		if chunk.EOF {
			break
		}
	}
	prefix := "host/sessions/" + id + "/"
	clock, _ := json.Marshal(map[string]any{"v": 1, "epoch": epoch, "brackets": brackets})
	if int64(len(data)+len(clock)) > *budget {
		return []string{id + ": the bounded original trace budget is full"}
	}
	*budget -= int64(len(data) + len(clock))
	files[prefix+"guest-trace.jsonl"] = collected{data: data, made: true, text: true}
	files[prefix+"guest-clock.json"] = collected{data: clock, made: true, text: true}
	if m.Wall == nil {
		return nil
	} // The NONE baseline shares this host clock.
	upper, upperErr := HolderRequest(id, api.OpDebugTraceUpperV1, struct {
		ID string `json:"id"`
	}{ID: id}, holderWait)
	var bounds api.TraceUpperSnapshotV1
	if upperErr != nil || json.Unmarshal(upper, &bounds) != nil || bounds.Version != 1 || bounds.Session != id || bounds.Generation == "" || len(bounds.Samples) > 2048 {
		return []string{fmt.Sprintf("%s: no original host-clock output bounds: %v", id, upperErr)}
	}
	if int64(len(upper)) > *budget {
		return []string{id + ": the bounded original trace budget is full"}
	}
	*budget -= int64(len(upper))
	files[prefix+"host-upper.json"] = collected{data: upper, made: true, text: true}
	return nil
}
