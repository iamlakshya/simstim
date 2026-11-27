package api

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// FeaturePullOutputProgress is negotiated only on the retained supervisor line.
const FeaturePullOutputProgress = "pull.output-progress-v1"

// PullOutputProgressEvent identifies actual trusted output acknowledgements.
const PullOutputProgressEvent = "pull_output_progress"

// PullOutputProgressMax bounds this small control frame, including its type.
const PullOutputProgressMax = 512

// PullOutputProgress renews only an existing export's exact current read.
// Epoch and Request reuse existing begin/read correlation IDs, never a new
// allocator. The authenticated retained connection supplies owner identity.
type PullOutputProgress struct {
	Event   string `json:"event"`
	ID      string `json:"id"`
	Token   string `json:"token"`
	Epoch   uint64 `json:"epoch"`
	Request uint64 `json:"request"`
	Next    int64  `json:"next"`
}

// Validate checks scalar form before any existing manager state is consulted.
func (p PullOutputProgress) Validate() error {
	if p.Event != PullOutputProgressEvent || !ValidSessionID(p.ID) || !ValidSessionID(p.Token) || len(p.Token) != 26 || p.Next < 0 {
		return fmt.Errorf("invalid owned pull output progress. Try: use the matching live read scope")
	}
	return nil
}

// EncodePullOutputProgress encodes the exact negotiated small event shape.
func EncodePullOutputProgress(p PullOutputProgress) ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	data, err := json.Marshal(p)
	if err != nil || len(data)+1 > PullOutputProgressMax {
		return nil, fmt.Errorf("pull progress exceeds its frame bound. Try: use the current helper")
	}
	return data, nil
}

// DecodePullOutputProgress uses the existing strict object decoder. It creates
// no request, export, lease, timer, resource, secret or success authority.
func DecodePullOutputProgress(data []byte) (PullOutputProgress, error) {
	var p PullOutputProgress
	if len(data)+1 > PullOutputProgressMax {
		return p, fmt.Errorf("pull progress exceeds its frame bound. Try: use the current helper")
	}
	fields, err := JSONObjectFields(data, "event", "id", "token", "epoch", "request", "next")
	if err != nil || len(fields) != 6 {
		return p, fmt.Errorf("invalid pull progress fields. Try: use the negotiated current helper")
	}
	for _, name := range []string{"event", "id", "token", "epoch", "request", "next"} {
		value, present := fields[name]
		if !present || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return p, fmt.Errorf("pull progress omits a required scalar. Try: use the negotiated current helper")
		}
	}
	if err = json.Unmarshal(data, &p); err != nil {
		return p, fmt.Errorf("decode pull output progress: %w. Try: use the current helper", err)
	}
	return p, p.Validate()
}
