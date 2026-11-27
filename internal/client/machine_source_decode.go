package client

import (
	"encoding/json"
	"reflect"

	"github.com/BurntSushi/toml"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/configdata"
)

// The SAME C9 tree owns this source's key identity and values only. A change
// to another host, unrelated policy or membership marker is not a change to
// this opaque source. JSON strings retain the original object value bytes.
type machineSourceOpaque struct {
	json  string
	keys  []string
	table any
}

func cloneMachineSourceValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			out[key] = cloneMachineSourceValue(item)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for index, item := range v {
			out[index] = cloneMachineSourceValue(item)
		}
		return out
	case []map[string]any:
		out := make([]map[string]any, len(v))
		for index, item := range v {
			out[index] = cloneMachineSourceValue(item).(map[string]any)
		}
		return out
	default:
		return value
	}
}

// decodeMachineSourceHosts consumes ONLY the table already produced by the
// SAME C9 decoder/schema. It never reads a file or reparses TOML itself.
func decodeMachineSourceHosts(data string, dst *hostsFile) (toml.MetaData, error) {
	meta, table, err := configdata.DecodeTable(data, dst)
	if err != nil {
		return meta, err
	}
	for _, hostKey := range configdata.InspectConfigTable(table, reflect.TypeOf(hostsFile{})).Keys {
		if hostKey.Name != "host" || hostKey.Type == nil {
			continue
		}
		var rows []map[string]any
		switch value := table[hostKey.Spelling].(type) {
		case []map[string]any:
			rows = value
		case []any:
			for _, row := range value {
				if item, ok := row.(map[string]any); ok {
					rows = append(rows, item)
				}
			}
		}
		for index, row := range rows {
			if index >= len(dst.Hosts) {
				break
			}
			h := &dst.Hosts[index]
			if h.Machine == nil || h.Machine.Source == nil {
				continue
			}
			for _, machineKey := range configdata.InspectConfigTable(row, reflect.TypeOf(Host{})).Keys {
				if machineKey.Name != "machine" || machineKey.Type == nil {
					continue
				}
				machine, ok := row[machineKey.Spelling].(map[string]any)
				if !ok {
					continue
				}
				for _, sourceKey := range configdata.InspectConfigTable(machine, reflect.TypeOf(Machine{})).Keys {
					if sourceKey.Name != "source" || sourceKey.Type == nil {
						continue
					}
					source, ok := machine[sourceKey.Spelling].(map[string]any)
					if !ok {
						continue
					}
					alias := hostKey.Spelling != "host" || machineKey.Spelling != "machine" || sourceKey.Spelling != "source"
					if alias || h.Machine.Source.Version != MachineSourceVersion || machineSourceTableOpaque(source, reflect.TypeOf(MachineSourceProvenance{})) {
						h.Machine.Source.opaque = &machineSourceOpaque{keys: []string{hostKey.Spelling, machineKey.Spelling, sourceKey.Spelling}, table: cloneMachineSourceValue(source)}
					}
				}
			}
		}
	}
	return meta, nil
}

func machineSourceTableOpaque(table map[string]any, typ reflect.Type) bool {
	for _, key := range configdata.InspectConfigTable(table, typ).Keys {
		if key.Type == nil || key.Spelling != key.Name || key.Conflict != "" {
			return true
		}
		if key.Name == "config" {
			child, ok := table[key.Spelling].(map[string]any)
			if !ok || machineSourceTableOpaque(child, reflect.TypeOf(MachineConfigSource{})) {
				return true
			}
		}
	}
	return false
}

func (s *MachineSourceProvenance) UnmarshalJSON(data []byte) error {
	type wire MachineSourceProvenance
	var value wire
	fields, err := api.JSONObjectFields(data, "Version", "Kind", "ResourceID", "Endpoint", "EngineID", "Config")
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	opaque := value.Version != MachineSourceVersion
	for key, raw := range fields {
		switch key {
		case "Version", "Kind", "ResourceID", "Endpoint", "EngineID":
		case "Config":
			config, err := api.JSONObjectFields(raw, "Role", "Path", "ObjectID", "ParentID")
			if err != nil {
				return err
			}
			for name := range config {
				if name != "Role" && name != "Path" && name != "ObjectID" && name != "ParentID" {
					opaque = true
				}
			}
		default:
			opaque = true
		}
	}
	*s = MachineSourceProvenance(value)
	if opaque {
		s.opaque = &machineSourceOpaque{json: string(data)}
	}
	return nil
}

func (s MachineSourceProvenance) MarshalJSON() ([]byte, error) {
	if s.opaque != nil {
		if s.opaque.json != "" {
			return []byte(s.opaque.json), nil
		}
		return json.Marshal(s.opaque.table)
	}
	type wire MachineSourceProvenance
	return json.Marshal(wire(s))
}
