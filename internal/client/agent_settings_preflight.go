package client

import (
	"reflect"

	"github.com/iamlakshya/simstim/internal/configdata"
	"github.com/iamlakshya/simstim/internal/tomlinspect"
)

// preflight uses the SAME final-map guard and descriptor matching. All
// possible aliases are checked before returning the first map-shape error.
func (node *agentSchemaNode) preflight(value tomlinspect.ValueCursor, shape *error) error {
	switch node.typ.Kind() {
	case reflect.Struct:
		if value.Type() != tomlinspect.TableValue {
			return nil
		}
		clear(node.slots)
		table, err := value.Table()
		if err != nil {
			return err
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
				return nil
			}
			if node.slots[index].present {
				return errAgentSettingsAlias
			}
			node.slots[index] = agentSchemaSlot{present: true, value: child}
			if node.children[index].containers {
				return node.children[index].preflight(child, shape)
			}
			return nil
		})
	case reflect.Map:
		if err := configdata.CheckMapShape(node.typ, value.Type() == tomlinspect.TableValue); err != nil {
			if *shape == nil {
				*shape = err
			}
			return nil
		}
		if !node.elem.containers {
			return nil
		}
		table, err := value.Table()
		if err != nil {
			return nil
		}
		return table.Walk(func(_ tomlinspect.PathCursor, child tomlinspect.ValueCursor) error {
			return node.elem.preflight(child, shape)
		})
	case reflect.Slice, reflect.Array:
		if value.Type() != tomlinspect.ArrayValue || !node.elem.containers {
			return nil
		}
		array, err := value.Array()
		if err != nil {
			return err
		}
		return array.Walk(func(_ uint32, _ tomlinspect.PathCursor, child tomlinspect.ValueCursor) error {
			return node.elem.preflight(child, shape)
		})
	}
	return nil
}
