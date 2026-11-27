package vmneed

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// UnsupportedVersionError preserves opaque future metadata for the protected
// manifest owner. It cannot be used for launch, grants or interpretation.
type UnsupportedVersionError struct {
	Version int
	Raw     json.RawMessage
}

// Error reports the unsupported version without interpreting its payload.
func (e *UnsupportedVersionError) Error() string {
	return fmt.Sprintf("VM Need version %d is unsupported", e.Version)
}

// DecodeV1 is the one strict codec. Existing transport/body bounds apply
// before it. It rejects aliases and duplicates, then validates the complete
// typed Need for the actual host architecture. It does not authenticate it.
func DecodeV1(data []byte, arch string) (*NeedV1, error) {
	version, err := envelopeVersion(data)
	if err != nil {
		return nil, err
	}
	if version != 1 {
		if version < 1 {
			return nil, fmt.Errorf("the VM Need version is not positive")
		}
		return nil, &UnsupportedVersionError{Version: version, Raw: append(json.RawMessage(nil), data...)}
	}
	if err := canonicalV1(data); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var need NeedV1
	if err := decoder.Decode(&need); err != nil {
		return nil, fmt.Errorf("decode VM Need: %w", err)
	}
	if err := need.Validate(arch); err != nil {
		return nil, err
	}
	return &need, nil
}

// EncodeV1 is the matching validated writer. No other encoder/normaliser
// changes a recorded Need's pins on its way to transport or protected storage.
func EncodeV1(need NeedV1, arch string) ([]byte, error) {
	if err := need.Validate(arch); err != nil {
		return nil, err
	}
	data, err := json.Marshal(need)
	if err != nil {
		return nil, fmt.Errorf("encode VM Need: %w", err)
	}
	return data, nil
}

func envelopeVersion(data []byte) (int, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return 0, fmt.Errorf("the VM Need is not one JSON object")
	}
	seen := map[string]bool{}
	version := 0
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return 0, err
		}
		key, ok := token.(string)
		if !ok || seen[key] {
			return 0, fmt.Errorf("the VM Need has a duplicate or invalid outer field")
		}
		seen[key] = true
		if strings.EqualFold(key, "version") && key != "version" {
			return 0, fmt.Errorf("the VM Need version uses a non-canonical field alias")
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return 0, fmt.Errorf("decode VM Need envelope: %w", err)
		}
		if key == "version" {
			if err := json.Unmarshal(raw, &version); err != nil {
				return 0, fmt.Errorf("the VM Need version is not an integer")
			}
		}
	}
	if _, err := decoder.Token(); err != nil {
		return 0, err
	}
	var more any
	if err := decoder.Decode(&more); !errors.Is(err, io.EOF) {
		return 0, fmt.Errorf("the VM Need has trailing content")
	}
	if !seen["version"] {
		return 0, fmt.Errorf("the VM Need has no canonical version")
	}
	return version, nil
}
