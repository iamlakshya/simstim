package client

import (
	"bytes"
	"encoding/json"
)

func unknownLedgerFields(fields map[string]json.RawMessage, known ...string) map[string]json.RawMessage {
	var extra map[string]json.RawMessage
	for name, value := range fields {
		found := false
		for _, key := range known {
			if name == key {
				found = true
				break
			}
		}
		if !found {
			if extra == nil {
				extra = map[string]json.RawMessage{}
			}
			extra[name] = value
		}
	}
	return extra
}
func ledgerHasUnknownFields(f grantsFile) bool {
	if len(f.unknown) > 0 {
		return true
	}
	for _, row := range f.Grants {
		if len(row.unknown) > 0 {
			return true
		}
	}
	return false
}
func encodeOpaqueGrantsFile(f grantsFile) ([]byte, error) {
	var rows bytes.Buffer
	rows.WriteByte('[')
	for i, row := range f.Grants {
		if i > 0 {
			rows.WriteByte(',')
		}
		known, err := json.Marshal(row)
		if err != nil {
			return nil, err
		}
		fields, err := sandboxObject(known)
		if err != nil {
			return nil, badGrantStateError(grantsFileName)
		}
		for name, value := range row.unknown {
			fields[name] = value
		}
		raw, err := marshalSandboxObject(fields)
		if err != nil {
			return nil, badGrantStateError(grantsFileName)
		}
		rows.Write(raw)
	}
	rows.WriteByte(']')
	fields := map[string]json.RawMessage{"version": json.RawMessage("1"), "grants": rows.Bytes()}
	for name, value := range f.unknown {
		fields[name] = value
	}
	raw, err := marshalSandboxObject(fields)
	if err != nil {
		return nil, badGrantStateError(grantsFileName)
	}
	return append(raw, '\n'), nil
}
