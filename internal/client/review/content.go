package review

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"

	"github.com/BurntSushi/toml"
)

func contentChanged(checker string, change Change) (bool, error) {
	switch checker {
	case "scripts":
		before, err := jsonField(change.Before, "scripts")
		if err != nil {
			return false, err
		}
		after, err := jsonField(change.After, "scripts")
		return !reflect.DeepEqual(before, after), err
	case "python-build":
		before, err := pythonBuild(change.Before)
		if err != nil {
			return false, err
		}
		after, err := pythonBuild(change.After)
		return !reflect.DeepEqual(before, after), err
	case "attributes-filter":
		for _, line := range strings.Split(string(change.Added), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			for _, field := range strings.Fields(line) {
				attribute := strings.TrimLeft(field, "-!")
				if attribute == "filter" || strings.HasPrefix(attribute, "filter=") {
					return true, nil
				}
			}
		}
		return false, nil
	default:
		return false, fmt.Errorf("unknown static content checker")
	}
}

func jsonField(data []byte, key string) (any, error) {
	if len(data) == 0 {
		return nil, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil {
		return nil, fmt.Errorf("parse package JSON: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("package JSON has trailing data")
	}
	return object[key], nil
}

func pythonBuild(data []byte) (any, error) {
	result := map[string]any{"build-system": nil, "scripts": nil, "gui-scripts": nil, "entry-points": nil}
	if len(data) == 0 {
		return result, nil
	}
	var object map[string]any
	if _, err := toml.Decode(string(data), &object); err != nil {
		return nil, fmt.Errorf("parse Python build settings: %w", err)
	}
	result["build-system"] = object["build-system"]
	if project, ok := object["project"].(map[string]any); ok {
		result["scripts"] = project["scripts"]
		result["gui-scripts"] = project["gui-scripts"]
		result["entry-points"] = project["entry-points"]
	}
	return result, nil
}
