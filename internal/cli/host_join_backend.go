package cli

import (
	"bytes"
	"encoding/json"
	"io"

	"github.com/iamlakshya/simstim/internal/client"
)

// These are local checked preparation choices. They carry no release pins,
// executable paths, inventory, provider authority or saved-host permission.
type localJoinOptionsV1 struct {
	Account            string   `json:"account"`
	Addresses          []string `json:"addresses,omitempty"`
	Port               uint16   `json:"port"`
	CPUs               int      `json:"cpus,omitempty"`
	MemoryBytes        int64    `json:"memory_bytes,omitempty"`
	AllowACSleepChange bool     `json:"allow_ac_sleep_change,omitempty"`
	StartMode          string   `json:"start_mode,omitempty"`
	AgentUser          string   `json:"agent_user,omitempty"`
}
type localJoinPlanV1 interface{ PlanText() string }
type localJoinEnvelopeV1 struct {
	Version int                `json:"version"`
	Kind    string             `json:"kind"`
	Invite  string             `json:"invite,omitempty"`
	Options localJoinOptionsV1 `json:"options"`
}

func encodeLocalJoinEnvelopeV1(kind, invite string, options localJoinOptionsV1) (io.Reader, error) {
	body, err := json.Marshal(localJoinEnvelopeV1{1, kind, invite, options})
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(body), nil
}
func decodeLocalJoinEnvelopeV1(in io.Reader, kind string) (localJoinEnvelopeV1, error) {
	var request localJoinEnvelopeV1
	body, err := io.ReadAll(io.LimitReader(in, (16<<10)+1))
	if err != nil {
		return request, err
	}
	if len(body) > 16<<10 {
		return request, localJoinPayloadErrorV1()
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&request); err != nil {
		return request, localJoinPayloadErrorV1()
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || request.Version != 1 || request.Kind != kind {
		return request, localJoinPayloadErrorV1()
	}
	return request, nil
}
func localJoinPayloadErrorV1() error {
	return &client.UserError{Msg: "the original local machine request is malformed or uses another version", Hint: "Run host join or host leave through the matching current client.", Code: "join_request", Kind: client.KindRefused}
}
