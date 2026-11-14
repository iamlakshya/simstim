package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/iamlakshya/simstim/internal/proto"
)

// PolicySourcePin records locally selected source identities and original
// session values. Laptop repository/profile values refresh from local config.
// It carries no host tier, secret value or exposure approval.
type PolicySourcePin struct {
	Layers    []LayerRef         `json:"layers"`
	Session   LayerValues        `json:"session"`
	Resources *PolicyResourcePin `json:"resources,omitempty"`
}

// PolicyResourcePin preserves a resource ceiling resolved at creation. It is
// local metadata, never a secret, network grant or host-supplied authority.
type PolicyResourcePin struct {
	Version int   `json:"version"`
	PullMax int64 `json:"pull_max"`
}

// PinPolicySources selects only the immutable metadata needed at a next start.
// The caller gives locally materialised layers, never a host response.
func PinPolicySources(layers []Layer) (*PolicySourcePin, error) {
	if len(layers) == 0 {
		return nil, nil
	}
	pin := &PolicySourcePin{}
	for _, layer := range layers {
		pin.Layers = append(pin.Layers, LayerRef{Kind: layer.Kind, Name: layer.Name, Source: layer.Source})
		if layer.Kind == LayerSession {
			pin.Session = CloneLayerValues(layer.LayerValues)
		}
	}
	if err := validatePolicyPin(pin); err != nil {
		return nil, err
	}
	return pin, nil
}

// PinPolicySourcesWithResources adds the already resolved local creation
// ceiling to the same source pin. It never creates a pin for legacy nil layers.
func PinPolicySourcesWithResources(layers []Layer, resources PolicyResourcePin) (*PolicySourcePin, error) {
	pin, err := PinPolicySources(layers)
	if err != nil || pin == nil {
		return pin, err
	}
	pin.Resources = &resources
	if err := validatePolicyPin(pin); err != nil {
		return nil, err
	}
	return pin, nil
}

// DecodePolicySourcePin reads a typed view without granting it authority. Its
// owner retains the original raw bytes, including unsupported future metadata.
// A caller may never save this view over the original immutable field.
func DecodePolicySourcePin(raw json.RawMessage) (*PolicySourcePin, error) {
	if len(raw)+1 > proto.MaxFrame {
		return nil, fmt.Errorf("policy pin exceeds the creation frame bound")
	}
	if len(raw) == 0 {
		return nil, nil
	}
	if err := strictPinJSON(raw, reflect.TypeFor[PolicySourcePin]()); err != nil {
		return nil, err
	}
	var pin PolicySourcePin
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&pin); err != nil {
		return nil, fmt.Errorf("decode policy source pin: %w", err)
	}
	if err := validatePolicyPin(&pin); err != nil {
		return nil, err
	}
	return &pin, nil
}

func validatePolicyPin(pin *PolicySourcePin) error {
	if r := pin.Resources; r != nil {
		// ParseSize uses this same positive range. A raw integer must not
		// bypass its upper bound or turn the ceiling into an unlimited value.
		if r.Version != 1 || r.PullMax <= 0 || r.PullMax >= 1<<62 {
			return fmt.Errorf("unsupported or invalid local policy resource projection")
		}
	}
	seen := map[LayerRef]bool{}
	kinds := map[LayerKind]bool{}
	for _, ref := range pin.Layers {
		if !ValidSelectedLayerRef(ref) || seen[ref] || kinds[ref.Kind] && ref.Kind != LayerRepo {
			return fmt.Errorf("invalid local policy source reference")
		}
		seen[ref], kinds[ref.Kind] = true, true
	}
	encoded, err := json.Marshal(pin)
	if err != nil {
		return fmt.Errorf("encode source pin bound: %w", err)
	}
	if len(encoded)+1 > proto.MaxFrame {
		return fmt.Errorf("policy pin exceeds the creation frame bound")
	}
	if !kinds[LayerSession] {
		return fmt.Errorf("the local source pin has no session layer")
	}
	return nil
}

// strictPinJSON shares H5's one duplicate/alias parser and follows real tags.
// It validates structure only; the canonical merge validates value semantics.
func strictPinJSON(raw json.RawMessage, typ reflect.Type) error {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return ErrJSONObject
	}
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	switch typ.Kind() {
	case reflect.Slice:
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return fmt.Errorf("read pin items: %w", err)
		}
		for _, item := range items {
			if err := strictPinJSON(item, typ.Elem()); err != nil {
				return err
			}
		}
	case reflect.Map:
		fields, err := JSONObjectFields(raw)
		if err != nil {
			return err
		}
		for _, value := range fields {
			if err := strictPinJSON(value, typ.Elem()); err != nil {
				return err
			}
		}
	case reflect.Struct:
		fields := pinJSONFields(typ)
		names := make([]string, 0, len(fields))
		for name := range fields {
			names = append(names, name)
		}
		values, err := JSONObjectFields(raw, names...)
		if err != nil {
			return err
		}
		for name, value := range values {
			child, known := fields[name]
			if !known {
				return fmt.Errorf("unsupported policy pin field %q", name)
			}
			if err := strictPinJSON(value, child); err != nil {
				return err
			}
		}
	}
	return nil
}

func pinJSONFields(typ reflect.Type) map[string]reflect.Type {
	fields := map[string]reflect.Type{}
	for i := range typ.NumField() {
		field := typ.Field(i)
		if field.PkgPath != "" {
			continue
		}
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if name == "-" {
			continue
		}
		if field.Anonymous && name == "" {
			for name, child := range pinJSONFields(field.Type) {
				fields[name] = child
			}
			continue
		}
		if name == "" {
			name = field.Name
		}
		fields[name] = field.Type
	}
	return fields
}
