package client

import (
	"reflect"

	"github.com/BurntSushi/toml"

	"github.com/iamlakshya/simstim/internal/configdata"
)

// ConfigKey is one key matched by the shared TOML schema inspector.
type ConfigKey = configdata.ConfigKey

// ConfigTable is the shared schema view for one TOML table.
type ConfigTable = configdata.ConfigTable

// InspectConfigTable delegates to the one decoder/checker schema rule.
func InspectConfigTable(table map[string]any, typ reflect.Type) ConfigTable {
	return configdata.InspectConfigTable(table, typ)
}

// decodeConfig keeps ordinary loader callsites on the shared decoder.
func decodeConfig(data string, dst any) (toml.MetaData, error) { return configdata.Decode(data, dst) }

type configAliasError = configdata.AliasError

type configField struct {
	name   string
	typ    reflect.Type
	index  []int
	tagged bool
}

// configFields retains the internal test view of the actual shared schema.
func configFields(typ reflect.Type) []configField {
	var out []configField
	for _, field := range configdata.Fields(typ) {
		out = append(out, configField{name: field.Name, typ: field.Type, index: field.Index, tagged: field.Tagged})
	}
	return out
}
