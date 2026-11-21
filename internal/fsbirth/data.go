// Package fsbirth observes native filesystem object generations. Its data
// describes no runtime, operation, source, cleanup permission or ownership.
package fsbirth

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// DataV2 is an opaque kernel observation stored by a resource's actual mkdir
// producer. A consumer must compare it with its current rooted object. Data
// from a missing, legacy or unrelated path cannot construct an original birth.
type DataV2 struct {
	Version    int      `json:"v"`
	Filesystem int64    `json:"filesystem"`
	FSID       [2]int32 `json:"fsid"`
	Type       int32    `json:"type"`
	Handle     []byte   `json:"handle"`
}

// Known reports only that the finite data shape is recognised. It grants no
// ownership; callers still need actual producer provenance and native compare.
func (data *DataV2) Known() bool {
	return data != nil && data.Version == 2 && data.Filesystem != 0 && data.FSID != [2]int32{} && data.Type > 0 && len(data.Handle) > 0 && len(data.Handle) <= 128
}

// Same compares known opaque observations without interpreting handle bytes.
func Same(first, second *DataV2) bool {
	return first.Known() && second.Known() && first.Filesystem == second.Filesystem && first.FSID == second.FSID && first.Type == second.Type && bytes.Equal(first.Handle, second.Handle)
}

// UnmarshalJSON rejects unknown/case-aliased/duplicate fields and a filesystem
// ID of another size. A recognised shape remains data, never a source grant.
func (data *DataV2) UnmarshalJSON(raw []byte) error {
	if !json.Valid(raw) {
		return errors.New("invalid native filesystem generation data")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return errors.New("native filesystem generation is not an object")
	}
	fields := map[string]json.RawMessage{}
	for decoder.More() {
		token, err := decoder.Token()
		name, ok := token.(string)
		if err != nil || !ok {
			return errors.New("native filesystem generation has an unknown field name")
		}
		switch name {
		case "v", "filesystem", "fsid", "type", "handle":
		default:
			return errors.New("native filesystem generation has an unknown or aliased field")
		}
		if _, duplicate := fields[name]; duplicate {
			return errors.New("native filesystem generation has a duplicate field")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
		}
		fields[name] = value
	}
	if _, err := decoder.Token(); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("native filesystem generation has trailing data")
	}
	var identity []int32
	if json.Unmarshal(fields["fsid"], &identity) != nil || len(identity) != 2 {
		return errors.New("native filesystem generation has another filesystem ID size")
	}
	type plain DataV2
	var decoded plain
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	*data = DataV2(decoded)
	return nil
}
