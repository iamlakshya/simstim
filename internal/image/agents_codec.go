package image

import (
	"encoding/json"

	"github.com/iamlakshya/simstim/internal/api"
)

// UnmarshalJSON refuses ambiguous or future producer inventory. The canonical
// JSON barrier owns parsing; this adapter only checks this closed role schema.
func (d *AgentDiskV1) UnmarshalJSON(raw []byte) error {
	fields, err := agentsFields(raw, "version", "arch", "source_digest", "content", "agents")
	if err != nil {
		return err
	}
	if _, err := agentsFields(fields["content"], "name", "sha256", "size"); err != nil {
		return err
	}
	type plain AgentDiskV1
	var value plain
	if json.Unmarshal(raw, &value) != nil || value.Agents == nil {
		return agentsDiskError("the producer inventory is malformed")
	}
	result := AgentDiskV1(value)
	if err := result.Validate(result.Arch); err != nil {
		return err
	}
	*d = result
	return nil
}

func agentsFields(raw []byte, names ...string) (map[string]json.RawMessage, error) {
	fields, err := api.JSONObjectFields(raw, names...)
	if err != nil || len(fields) != len(names) {
		return nil, agentsDiskError("agents metadata is malformed, incomplete or from an unknown version")
	}
	for _, name := range names {
		if _, found := fields[name]; !found {
			return nil, agentsDiskError("agents metadata has an unknown field")
		}
	}
	return fields, nil
}
