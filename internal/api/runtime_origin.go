package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/supply"
)

// FeatureRuntimeOriginV1 binds a VM projection to one original full producer.
const FeatureRuntimeOriginV1 = "runtime-origin-v1"

// MaxRuntimeOriginV1 bounds data inside the existing four-MiB frame.
const MaxRuntimeOriginV1 = proto.MaxFrame / 4

// RuntimeOriginV1 is decoded original full runtime DATA, never a producer.
// Wire callers use the sole bounded strict decoder below before typed use.
type RuntimeOriginV1 struct {
	V        int             `json:"v"`
	Manifest supply.Manifest `json:"manifest"`
}

// DecodeRuntimeOriginV1 rejects ambiguous fields before any typed allocation.
// A nil advisory list is established supply data and retains its exact shape.
func DecodeRuntimeOriginV1(raw json.RawMessage) (*RuntimeOriginV1, error) {
	if len(raw) == 0 || len(raw) > MaxRuntimeOriginV1 {
		return nil, RuntimeOriginErrorV1()
	}
	fields, err := JSONObjectFields(raw, "v", "manifest")
	if err != nil || len(fields) != 2 || fields["v"] == nil || fields["manifest"] == nil || !ExactJSONFields(raw, RuntimeOriginV1{}) {
		return nil, RuntimeOriginErrorV1()
	}
	manifest, err := JSONObjectFields(fields["manifest"])
	if err != nil || manifest["items"] == nil || bytes.Equal(bytes.TrimSpace(manifest["items"]), []byte("null")) {
		return nil, RuntimeOriginErrorV1()
	}
	for key, value := range manifest {
		if key == "advisories" && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			continue
		}
		if !runtimeOriginNoNullsV1(value) {
			return nil, RuntimeOriginErrorV1()
		}
	}
	var value RuntimeOriginV1
	if json.Unmarshal(raw, &value) != nil || value.V != 1 || len(value.Manifest.Items) == 0 || value.Manifest.Validate() != nil {
		return nil, RuntimeOriginErrorV1()
	}
	return &value, nil
}

func runtimeOriginNoNullsV1(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return false
	}
	switch raw[0] {
	case '{':
		fields, err := JSONObjectFields(raw)
		if err != nil {
			return false
		}
		for _, value := range fields {
			if !runtimeOriginNoNullsV1(value) {
				return false
			}
		}
	case '[':
		var values []json.RawMessage
		if json.Unmarshal(raw, &values) != nil {
			return false
		}
		for _, value := range values {
			if !runtimeOriginNoNullsV1(value) {
				return false
			}
		}
	}
	return true
}

// EncodeRuntimeOriginV1 preserves the original vectors and uses the same sole
// decoder. Neither serialisation nor the full key creates producer authority.
func EncodeRuntimeOriginV1(value RuntimeOriginV1) (json.RawMessage, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, RuntimeOriginErrorV1()
	}
	if _, err := DecodeRuntimeOriginV1(raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func CloneRuntimeOriginV1(o *RuntimeOriginV1) *RuntimeOriginV1 {
	if o == nil {
		return nil
	}
	copy := *o
	copy.Manifest.Items = slices.Clone(o.Manifest.Items)
	copy.Manifest.Advisories = slices.Clone(o.Manifest.Advisories)
	return &copy
}
func RuntimeOriginErrorV1() error {
	return &proto.Error{Code: "runtime_origin", Message: fmt.Sprintf("the original runtime carrier must be exact version1 metadata of at most %d bytes", MaxRuntimeOriginV1), Hint: "Prepare the VM with a current client and the same connection's original complete runtime request."}
}
