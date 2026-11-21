package client

import (
	"errors"
	"os"
	"reflect"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/iamlakshya/simstim/internal/api"
)

// PolicyEditLocation returns a local file and its selected table's first line.
// It reads no secret value and creates no file, folder or approval.
func PolicyEditLocation(layer FileLayer) (string, int, error) {
	path := settingsPath()
	if layer.Kind == api.LayerHost {
		path = hostsPath()
	}
	if layer.Kind != api.LayerAll && layer.Kind != api.LayerHost && layer.Kind != api.LayerRepo && layer.Kind != api.LayerProfile {
		return "", 0, missingFileLayer()
	}
	if layer.Kind != api.LayerAll && layer.Name == "" {
		return "", 0, missingFileLayer()
	}
	raw, err := ReadConfigFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return path, 1, nil
	}
	if err != nil {
		return "", 0, err
	}
	return path, policyEditLine(string(raw), layer), nil
}

func policyEditLine(raw string, layer FileLayer) int {
	if layer.Kind == api.LayerAll {
		return 1
	}
	hostStart := 0
	for index, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			hostStart = 0
			var header map[string]any
			if _, err := toml.Decode(trimmed+"\n", &header); err != nil {
				continue
			}
			if layer.Kind == api.LayerHost {
				key := schemaKey(header, reflect.TypeFor[hostsFile](), "host")
				if _, ok := header[key].([]map[string]any); ok {
					hostStart = index + 1
				}
				continue
			}
			kind := "profiles"
			if layer.Kind == api.LayerRepo {
				kind = "repos"
			}
			key := schemaKey(header, reflect.TypeFor[Settings](), kind)
			parent, ok := header[key].(map[string]any)
			if ok {
				if _, ok := parent[layer.Name]; ok {
					return index + 1
				}
			}
			continue
		}
		if layer.Kind == api.LayerHost && hostStart != 0 {
			var value map[string]any
			if _, err := toml.Decode(trimmed+"\n", &value); err != nil {
				continue
			}
			key := schemaKey(value, reflect.TypeFor[Host](), "name")
			if value[key] == layer.Name {
				return hostStart
			}
		}
	}
	return 1
}
