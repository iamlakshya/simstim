package api

import (
	"encoding/json"
	"testing"
)

func TestExactJSONFieldsRetainsRawEnvelopeAndRejectsNestedDuplicates(t *testing.T) {
	type request struct {
		Version int             `json:"v"`
		Payload json.RawMessage `json:"request"`
	}
	for _, raw := range []string{
		`{"v":1,"request":{"scope":{"id":"one"},"items":[{"v":1}]}}`,
		`{"v":1,"request":[null,"text",1,true,{},[]]}`,
		`{"v":1,"request":null}`,
	} {
		if !ExactJSONFields([]byte(raw), request{}) {
			t.Fatalf("valid raw envelope refused: %s", raw)
		}
	}
	for _, raw := range []string{
		`{"v":1,"Request":{}}`,
		`{"v":1,"request":{},"request":{}}`,
		`{"v":1,"request":{"scope":{"id":"one","id":"two"}}}`,
		`{"v":1,"request":[{"scope":{"id":1,"id":2}}]}`,
	} {
		if ExactJSONFields([]byte(raw), request{}) {
			t.Fatalf("alias or nested duplicate accepted: %s", raw)
		}
	}
	// The raw wrapper does not close its payload schema. The actual consumer
	// applies its sole DTO after the wrapper check, including exact aliases.
	type payload struct {
		ID string `json:"id"`
	}
	raw := []byte(`{"v":1,"request":{"ID":"one"}}`)
	var decoded request
	if !ExactJSONFields(raw, request{}) || json.Unmarshal(raw, &decoded) != nil || ExactJSONFields(decoded.Payload, payload{}) {
		t.Fatal("a raw wrapper supplied nested field authority")
	}
}
