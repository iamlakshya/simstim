package client

import (
	"encoding"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/iamlakshya/simstim/internal/configdata"
	"github.com/iamlakshya/simstim/internal/tomlinspect"
)

var (
	errAgentSettingsType   = errors.New("config.toml has a known value of the wrong type")
	errAgentSettingsAlias  = errors.New("config.toml has two spellings of one known field")
	errAgentSettingsSchema = errors.New("the canonical settings schema needs an updated streaming adapter")
)

type agentSchemaSlot struct {
	present bool
	value   tomlinspect.ValueCursor
}
type agentSchemaNode struct {
	typ                reflect.Type
	fields             []configdata.Field
	children           []*agentSchemaNode
	elem               *agentSchemaNode
	slots              []agentSchemaSlot
	secret, containers bool
}
type agentSchema struct {
	root   *agentSchemaNode
	nodes  map[reflect.Type]*agentSchemaNode // Code-owned types only; no config names/commands.
	active map[reflect.Type]bool
}

func newAgentSchema() (*agentSchema, error) {
	if MaxConfigSize != configdata.MaxFileSize || MaxConfigSize != tomlinspect.MaxInput {
		return nil, errAgentSettingsSchema
	}
	for _, name := range tierNames {
		if len(name) > 32 {
			return nil, errAgentSettingsSchema
		}
	}
	schema := &agentSchema{nodes: map[reflect.Type]*agentSchemaNode{}, active: map[reflect.Type]bool{}}
	root, err := schema.compile(reflect.TypeFor[Settings]())
	if err != nil {
		return nil, err
	}
	schema.root = root
	var definitions, tiers *agentSchemaNode
	for index, field := range root.fields {
		if field.Name == "secrets" {
			definitions = root.children[index]
		}
		if field.Name == "secret_tiers" {
			tiers = root.children[index]
		}
	}
	if definitions == nil || definitions.typ.Kind() != reflect.Map || definitions.elem == nil || !definitions.elem.secret || tiers == nil || tiers.typ.Kind() != reflect.Map || tiers.elem == nil || tiers.elem.typ.Kind() != reflect.String {
		return nil, errAgentSettingsSchema
	}
	return schema, nil
}

func (s *agentSchema) compile(typ reflect.Type) (*agentSchemaNode, error) {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if s.active[typ] {
		return nil, errAgentSettingsSchema
	}
	if node := s.nodes[typ]; node != nil {
		return node, nil
	}
	s.active[typ] = true
	defer delete(s.active, typ)
	node := &agentSchemaNode{typ: typ, secret: typ == reflect.TypeFor[SecretDefinition]()}
	if typ == reflect.TypeFor[time.Duration]() || typ == reflect.TypeFor[json.Number]() {
		return nil, errAgentSettingsSchema
	}
	s.nodes[typ] = node
	if !node.secret {
		for _, hook := range []reflect.Type{reflect.TypeFor[toml.Unmarshaler](), reflect.TypeFor[encoding.TextUnmarshaler]()} {
			if typ.Implements(hook) || reflect.PointerTo(typ).Implements(hook) {
				return nil, errAgentSettingsSchema
			}
		}
	}
	switch typ.Kind() {
	case reflect.Struct:
		node.containers = true
		node.fields = configdata.Fields(typ)
		node.children = make([]*agentSchemaNode, len(node.fields))
		node.slots = make([]agentSchemaSlot, len(node.fields))
		for index, field := range node.fields {
			child, err := s.compile(field.Type)
			if err != nil {
				return nil, err
			}
			node.children[index] = child
		}
	case reflect.Map:
		node.containers = true
		if typ.Key().Kind() != reflect.String && typ.Key().Kind() != reflect.Interface {
			return nil, errAgentSettingsSchema
		}
		var err error
		node.elem, err = s.compile(typ.Elem())
		if err != nil {
			return nil, err
		}
	case reflect.Slice, reflect.Array:
		node.containers = true
		var err error
		node.elem, err = s.compile(typ.Elem())
		if err != nil {
			return nil, err
		}
	case reflect.Interface:
		if typ.NumMethod() != 0 {
			return nil, errAgentSettingsSchema
		}
	case reflect.String, reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Float32, reflect.Float64:
	default:
		return nil, errAgentSettingsSchema
	}
	return node, nil
}

func (s *agentSchema) clear() {
	for _, node := range s.nodes {
		clear(node.slots)
	}
}

func (node *agentSchemaNode) field(name tomlinspect.Text) (int, error) {
	// Fields supplies the canonical embedding/declaration order. Exact
	// spelling wins before the same Unicode fold fallback as configdata.
	for index, field := range node.fields {
		match, err := name.Equal(field.Name)
		if err != nil {
			return -1, err
		}
		if match {
			return index, nil
		}
	}
	for index, field := range node.fields {
		match, err := name.EqualFold(field.Name)
		if err != nil {
			return -1, err
		}
		if match {
			return index, nil
		}
	}
	return -1, nil
}

func (node *agentSchemaNode) table(value tomlinspect.ValueCursor, strict bool) error {
	clear(node.slots)
	table, err := value.Table()
	if err != nil {
		return errAgentSettingsType
	}
	return table.Walk(func(path tomlinspect.PathCursor, child tomlinspect.ValueCursor) error {
		name, err := path.Name()
		if err != nil {
			return err
		}
		index, err := node.field(name)
		if err != nil {
			return err
		}
		if index < 0 {
			if strict {
				return secretDefinitionError("a secret definition contains an unknown field")
			}
			return nil
		}
		if node.slots[index].present {
			return errAgentSettingsAlias
		}
		node.slots[index] = agentSchemaSlot{present: true, value: child}
		return nil
	})
}

func (node *agentSchemaNode) slot(name string) (tomlinspect.ValueCursor, bool) {
	for index, field := range node.fields {
		if field.Name == name {
			return node.slots[index].value, node.slots[index].present
		}
	}
	return tomlinspect.ValueCursor{}, false
}
