package host

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
)

// checkRequestedWall is an early safety interlock. Sandbox starts belong
// to the wall launcher. This helper must never turn a requested wall off.
func checkRequestedWall(level string) error {
	switch level {
	case "", "none":
		return nil
	case "auto", "process", "gvisor", "microvm":
		return UserError("wall_unsupported", "this helper cannot start the requested wall yet",
			"Use a helper that supports sandbox starts, or explicitly choose an unwalled session.")
	default:
		return UserError("bad_wall", "the requested wall is not a known level",
			"Choose auto, none, process, gvisor or microvm.")
	}
}

// manifestUnwalled reads the future wall field without taking ownership
// of its persistence. All raw nested metadata stays in Manifest.extra.
// Only legacy absence, or an explicit valid none wall, permits an agent.
func manifestUnwalled(m *Manifest) bool {
	if m.Wall != nil {
		return m.Wall.Level == "none" && m.Wall.Driver == "none"
	}
	raw, present := m.extra["wall"]
	if !present {
		return true
	}
	if !canonicalAgentWall(raw) {
		return false
	}
	var wall api.WallInfo
	if err := json.Unmarshal(raw, &wall); err != nil {
		return false
	}
	return wall.Level == "none" && wall.Driver == "none"
}

func canonicalAgentWall(raw []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return false
	}
	seen := map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		name, ok := token.(string)
		if err != nil || !ok || seen[name] {
			return false
		}
		seen[name] = true
		lower := strings.ToLower(name)
		if (lower == "level" || lower == "driver") && name != lower {
			return false
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return false
		}
	}
	last, err := decoder.Token()
	if err != nil || last != json.Delim('}') {
		return false
	}
	return decoder.Decode(new(any)) == io.EOF
}

func checkManifestUnwalled(m *Manifest) error {
	if manifestUnwalled(m) {
		return nil
	}
	return UserError("wall_unsupported", "this helper cannot restart this session's wall",
		"Use a helper that supports this wall. Its saved wall has been kept.")
}
