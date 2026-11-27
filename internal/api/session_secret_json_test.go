package api

import (
	"encoding/json"
	"testing"
	"time"
)

func TestExactSecretJSONFieldsPromoteCanonicalLayerValuesAndTimeScalars(t *testing.T) {
	type request struct {
		Policy Policy    `json:"policy"`
		Until  time.Time `json:"until"`
	}
	encoded, err := json.Marshal(request{Until: time.Unix(1, 0).UTC()})
	if err != nil || !ExactJSONFields(encoded, (*request)(nil)) {
		t.Fatal("canonical promoted Policy fields/time scalars were refused")
	}
	for _, raw := range []string{
		`{"policy":{"network":{}}}`, `{"policy":{"sandbox":{}}}`, `{"policy":{"classifier":{}}}`,
	} {
		var decoded request
		if json.Unmarshal([]byte(raw), &decoded) != nil || !ExactJSONFields([]byte(raw), (*request)(nil)) {
			t.Fatal("canonical promoted layer-value field was lost")
		}
	}
	for _, raw := range []string{
		`{"Policy":{}}`, `{"policy":{"Network":{}}}`, `{"policy":{"LayerValues":{}}}`, `{"policy":{"network":{},"network":{}}}`,
	} {
		if ExactJSONFields([]byte(raw), (*request)(nil)) {
			t.Fatal("field alias or duplicate bypassed sole exact DTO grammar")
		}
	}
}

func TestExactSecretJSONFieldsDirectTagsShadowPromotedFields(t *testing.T) {
	type Embedded struct {
		Field string `json:"field"`
	}
	type target struct {
		*Embedded
		Field struct {
			Value string `json:"value"`
		} `json:"field"`
	}
	if !ExactJSONFields([]byte(`{"field":{"value":"fixture"}}`), (*target)(nil)) {
		t.Fatal("direct canonical tag did not shadow promoted metadata")
	}
	if ExactJSONFields([]byte(`{"field":{"Value":"fixture"}}`), (*target)(nil)) {
		t.Fatal("shadowed field bypassed nested exact spellings")
	}
}
