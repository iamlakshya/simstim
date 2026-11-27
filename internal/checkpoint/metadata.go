// Package checkpoint copies a factory-owned session disk. It does not grant
// runtime, stopped-session or filesystem authority from saved metadata.
package checkpoint

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// TimeLayout is the portable UTC leaf-name layout of a disk checkpoint.
const TimeLayout = "20060102T150405.000Z"

const maxMetadata = 64 << 10

// Ref is a guest's last claimed branch tip, not a trusted git fact.
type Ref struct {
	Name string `json:"name"`
	SHA  string `json:"sha"`
}

// Metadata accompanies the disk. Session IDs originate in trusted manifests;
// refs and the claimed turn remain guest claims after a restore.
type Metadata struct {
	Refs            []Ref             `json:"refs,omitempty"`
	AgentSessionIDs map[string]string `json:"agent_session_ids"`
	Turn            string            `json:"turn,omitempty"`
}

// Entry describes one complete checkpoint. Times sort newest first in List.
type Entry struct {
	Time     time.Time `json:"time"`
	Metadata Metadata  `json:"metadata"`
}

// EncodeMetadataV1 encodes bounded checkpoint data for an actual storage
// backend. Its bytes grant no source, snapshot or filesystem authority.
func EncodeMetadataV1(metadata Metadata) ([]byte, error) {
	return metadataBytes(metadata)
}

// DecodeMetadataV1 checks bounded checkpoint data from an actual backend.
// Decoding does not reconstruct any native source or snapshot capability.
func DecodeMetadataV1(data []byte) (Metadata, error) {
	return decodeMetadata(data)
}

func metadataBytes(value Metadata) ([]byte, error) {
	if len(value.Refs) > 256 || len(value.AgentSessionIDs) == 0 || len(value.AgentSessionIDs) > 256 || !boundedText(value.Turn, 500) {
		return nil, fmt.Errorf("checkpoint metadata exceeds its bound")
	}
	seen := make(map[string]bool, len(value.Refs))
	for _, ref := range value.Refs {
		if ref.Name == "" || !boundedText(ref.Name, 1024) || !gitObjectID(ref.SHA) {
			return nil, fmt.Errorf("checkpoint metadata has an invalid branch claim")
		}
		if seen[ref.Name] {
			return nil, fmt.Errorf("checkpoint metadata repeats a branch claim")
		}
		seen[ref.Name] = true
	}
	for session, id := range value.AgentSessionIDs {
		if session == "" || !boundedText(session, 128) || !boundedText(id, 1024) {
			return nil, fmt.Errorf("checkpoint metadata has an invalid session identity")
		}
	}
	data, err := json.Marshal(value)
	if err != nil || len(data) > maxMetadata {
		return nil, fmt.Errorf("checkpoint metadata exceeds its encoded bound")
	}
	return data, nil
}

func decodeMetadata(data []byte) (Metadata, error) {
	var value Metadata
	if len(data) > maxMetadata {
		return value, fmt.Errorf("checkpoint metadata exceeds its encoded bound")
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return value, fmt.Errorf("decode checkpoint metadata: %w", err)
	}
	if _, err := metadataBytes(value); err != nil {
		return Metadata{}, err
	}
	// A second JSON value is invalid, even if the first one was sound.
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Metadata{}, fmt.Errorf("checkpoint metadata holds more than one value")
	}
	return value, nil
}

func cloneMetadata(value Metadata) Metadata {
	out := Metadata{Refs: append([]Ref(nil), value.Refs...), Turn: value.Turn, AgentSessionIDs: make(map[string]string, len(value.AgentSessionIDs))}
	for session, id := range value.AgentSessionIDs {
		out.AgentSessionIDs[session] = id
	}
	return out
}

func boundedText(value string, limit int) bool {
	return len(value) <= limit && utf8.ValidString(value) && strings.IndexFunc(value, unicode.IsControl) < 0
}

func gitObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, ch := range value {
		if ch < '0' || ch > '9' && ch < 'a' || ch > 'f' {
			return false
		}
	}
	return true
}
