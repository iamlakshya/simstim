package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"reflect"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/proto"
)

// layerTable checks values through the same validator as the policy merge.
// The schema inspector chooses the decoder's exact field before its aliases.
// Checking each known key separately gives each finding its own source line.
func (c *checker) layerTable(t map[string]any, typ reflect.Type, p keyPath) {
	prefix := ""
	switch typ {
	case reflect.TypeFor[api.SandboxValues]():
		prefix = "sandbox"
	case reflect.TypeFor[api.NetworkValues]():
		prefix = "network"
	case reflect.TypeFor[api.ClassifierValues]():
		prefix = "classifier"
	default:
		return
	}
	for _, key := range client.InspectConfigTable(t, typ).Keys {
		if key.Type == nil || key.Conflict != "" {
			continue
		}
		here := p.field(key.Spelling, key.Name)
		if forbiddenLayerKey(here) {
			continue
		}
		var b bytes.Buffer
		value := map[string]any{prefix: map[string]any{key.Name: t[key.Spelling]}}
		if err := toml.NewEncoder(&b).Encode(value); err != nil {
			continue // The ordinary type check reports an unusable value.
		}
		var values api.LayerValues
		if _, err := toml.Decode(b.String(), &values); err != nil {
			continue // The ordinary type check reports a wrong type.
		}
		if err := egress.ValidateLayerValues(values); err != nil {
			message := err.Error()
			code := "bad_value"
			var pe *proto.Error
			if errors.As(err, &pe) {
				message = pe.Message
				if pe.Code == "release_age_invalid" || pe.Code == "package_level_invalid" {
					code = pe.Code
				}
			}
			c.add(here, finding{Code: code, Message: strings.TrimSuffix(message, ".")})
		}
	}
}

// layerForbiddenKey reports settings that a file layer may not hold. It is
// called before the ordinary value check, including for known schema fields.
func (c *checker) layerForbiddenKey(p keyPath) bool {
	if len(p) == 0 {
		return false
	}
	if !forbiddenLayerKey(p) {
		return false
	}
	c.add(p, finding{Code: "layer_key", Message: ref(p) + " cannot grant a destination; locks hold values and denies"})
	return true
}

func forbiddenLayerKey(p keyPath) bool {
	if len(p) < 2 {
		return false
	}
	last := p[len(p)-1].name
	locked := p[0].name == "lock" || len(p) > 1 && p[0].name == "host" && p[1].name == "lock"
	return locked && p[len(p)-2].name == "network" && last == "allow" || p[0].name == "profiles" && p[len(p)-2].name == "sandbox" && last == "profile"
}

// configMap checks typed table values recursively, including free profile
// and repository names. Scalar maps retain their ordinary type checks.
func (c *checker) configMap(v any, elem reflect.Type, p keyPath) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	for _, k := range slices.Sorted(maps.Keys(m)) {
		c.value(m[k], elem, p.add(k))
	}
	return true
}

// layerUnknownKey distinguishes a known setting in a forbidden place from
// an unknown spelling. The ordinary unknown-key suggestions remain intact.
func (c *checker) layerUnknownKey(p keyPath, spelling string) bool {
	name := strings.ToLower(spelling)
	layerRoot := len(p) == 2 && (p[0].name == "profiles" || p[0].name == "repos")
	forbidden := layerRoot && name == "lock"
	if layerRoot {
		probe := client.InspectConfigTable(map[string]any{spelling: nil}, reflect.TypeFor[client.Settings]())
		for _, key := range probe.Keys {
			forbidden = forbidden || key.Type != nil
		}
	}
	if fileLayerTable(p) {
		switch name {
		case "once", "for", "until", "tools", "id", "grant_id":
			forbidden = true
		}
	}
	if !forbidden {
		return false
	}
	c.add(p.add(spelling), finding{Code: "layer_key", Message: ref(p.add(spelling)) + " is not allowed in this layer"})
	return true
}

func fileLayerTable(p keyPath) bool {
	if len(p) == 0 {
		return false
	}
	switch p[0].name {
	case "sandbox", "network", "classifier", "lock":
		return true
	case "profiles", "repos":
		return len(p) > 1
	case "host":
		if len(p) > 1 {
			switch p[1].name {
			case "sandbox", "network", "classifier", "lock":
				return true
			}
		}
	}
	return false
}

// addLayers appends the current folder's applicable layers to a clean report.
// A broken file already has findings and cannot supply trustworthy layers.
func (r *configReport) addLayers() {
	r.Layers = []client.LayerDescription{}
	if r.count(severityError) > 0 {
		return
	}
	layers, err := client.ConfigLayerReport()
	if err != nil {
		r.Findings = append(r.Findings, finding{Path: tildeHome(client.ConfigFiles()[0].Path), Severity: severityError,
			Code: "bad_value", Message: client.CleanLine(strings.TrimSuffix(err.Error(), "."))})
		return
	}
	r.Layers = layers
}

// writeConfigLayers prints the same source descriptions as the JSON report.
func writeConfigLayers(w io.Writer, layers []client.LayerDescription) {
	if len(layers) == 0 {
		return
	}
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	fmt.Fprintf(w, "\nLayers for %s:\n", client.CleanPath(tildeHome(cwd)))
	for _, layer := range layers {
		label := string(layer.Kind)
		switch layer.Kind {
		case api.LayerAll:
			label = "all sessions"
		case api.LayerRepo:
			label = "repository"
		}
		value := layer.Name
		if layer.Source != "" {
			if value != "" {
				value += ": "
			}
			value += layer.Source
		}
		if value == "" {
			value = "none"
		}
		fmt.Fprintf(w, "  %-12s %s\n", client.CleanLine(label), client.CleanLine(value))
	}
}
