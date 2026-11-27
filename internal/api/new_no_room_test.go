package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNewNoRoomEnvelopeRejectsAmbiguousAndFutureControl(t *testing.T) {
	good := NewNoRoomEnvelopeV1{Settlement: NewNoRoomSettlementV1{Version: 1, RequestID: 7, SessionID: "abcd2345", HostID: strings.Repeat("a", 32), Attempt: strings.Repeat("b", 32), Outcome: "never-dispatched"}}
	body, err := json.Marshal(good)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeNewNoRoomEnvelopeV1(body); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(string) string{
		func(s string) string { return strings.Replace(s, `"v":1`, `"v":2`, 1) },
		func(s string) string { return strings.Replace(s, `"v":1`, `"v":1,"v":1`, 1) },
		func(s string) string { return strings.Replace(s, `"v":1`, `"V":1`, 1) },
		func(s string) string { return strings.Replace(s, `"never-dispatched"`, `"maybe-closed"`, 1) },
		func(s string) string { return strings.Replace(s, strings.Repeat("b", 32), "public-code", 1) },
		func(s string) string { return strings.Replace(s, `"request_id":7`, `"request_id":0`, 1) },
		func(s string) string {
			return strings.Replace(s, `"request_id":7`, `"request_id":7,"hash":"private"`, 1)
		},
	} {
		if _, err := DecodeNewNoRoomEnvelopeV1([]byte(change(string(body)))); err == nil {
			t.Fatal("invalid settlement accepted", change(string(body)))
		}
	}
}
