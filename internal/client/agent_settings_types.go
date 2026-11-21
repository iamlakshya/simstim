package client

import (
	"math"
	"reflect"

	"github.com/iamlakshya/simstim/internal/configdata"
	"github.com/iamlakshya/simstim/internal/tomlinspect"
)

func (node *agentSchemaNode) typed(value tomlinspect.ValueCursor) error {
	if node.hostSelector {
		return agentHostSelectorType(value)
	}
	if node.secret && value.Type() == tomlinspect.ScalarValue {
		kind, err := value.ScalarType()
		if err != nil {
			return err
		}
		if kind != tomlinspect.StringScalar {
			return errAgentSettingsType
		}
		return nil
	}
	switch node.typ.Kind() {
	case reflect.Interface:
		return nil
	case reflect.Struct:
		if value.Type() != tomlinspect.TableValue {
			return errAgentSettingsType
		}
		if err := node.table(value, node.secret); err != nil {
			return err
		}
		for index, slot := range node.slots {
			if slot.present {
				if err := node.children[index].typed(slot.value); err != nil {
					return err
				}
			}
		}
		return nil
	case reflect.Map:
		if err := configdata.CheckMapShape(node.typ, value.Type() == tomlinspect.TableValue); err != nil {
			return err
		}
		table, err := value.Table()
		if err != nil {
			return errAgentSettingsType
		}
		return table.Walk(func(_ tomlinspect.PathCursor, child tomlinspect.ValueCursor) error { return node.elem.typed(child) })
	case reflect.Array, reflect.Slice:
		if value.Type() != tomlinspect.ArrayValue {
			return errAgentSettingsType
		}
		array, err := value.Array()
		if err != nil {
			return err
		}
		if node.typ.Kind() == reflect.Array {
			length, err := array.Len()
			if err != nil {
				return err
			}
			if uint64(length) != uint64(node.typ.Len()) {
				return errAgentSettingsType
			}
		}
		return array.Walk(func(_ uint32, _ tomlinspect.PathCursor, child tomlinspect.ValueCursor) error {
			return node.elem.typed(child)
		})
	default:
		if value.Type() != tomlinspect.ScalarValue {
			return errAgentSettingsType
		}
		native, err := value.ScalarType()
		if err != nil {
			return err
		}
		kind := node.typ.Kind()
		if kind == reflect.String {
			if native == tomlinspect.StringScalar {
				return nil
			}
			return errAgentSettingsType
		}
		if kind == reflect.Bool {
			if native == tomlinspect.BoolScalar {
				return nil
			}
			return errAgentSettingsType
		}
		if kind == reflect.Int || kind == reflect.Int64 || kind == reflect.Uint || kind == reflect.Uint64 {
			if native == tomlinspect.IntegerScalar {
				return nil
			}
			return errAgentSettingsType
		}
		scalar, err := value.Scalar()
		if err != nil {
			return err
		}
		return node.scalar(scalar)
	}
}

func (node *agentSchemaNode) scalar(value tomlinspect.ScalarCursor) error {
	kind := node.typ.Kind()
	switch kind {
	case reflect.String:
		if value.Kind() == tomlinspect.StringScalar {
			return nil
		}
	case reflect.Bool:
		if value.Kind() == tomlinspect.BoolScalar {
			return nil
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		number, ok := value.Integer()
		if !ok {
			return errAgentSettingsType
		}
		switch kind {
		case reflect.Int8:
			if number < math.MinInt8 || number > math.MaxInt8 {
				return errAgentSettingsType
			}
		case reflect.Int16:
			if number < math.MinInt16 || number > math.MaxInt16 {
				return errAgentSettingsType
			}
		case reflect.Int32:
			if number < math.MinInt32 || number > math.MaxInt32 {
				return errAgentSettingsType
			}
		case reflect.Uint8:
			if number < 0 || uint64(number) > math.MaxUint8 {
				return errAgentSettingsType
			}
		case reflect.Uint16:
			if number < 0 || uint64(number) > math.MaxUint16 {
				return errAgentSettingsType
			}
		case reflect.Uint32:
			if number < 0 || uint64(number) > math.MaxUint32 {
				return errAgentSettingsType
			}
		}
		// The pin's int/uint64/uint cases have no extra range test.
		return nil
	case reflect.Float32, reflect.Float64:
		if number, ok := value.Float(); ok {
			if kind == reflect.Float32 && (number < -math.MaxFloat32 || number > math.MaxFloat32) {
				return errAgentSettingsType
			}
			return nil
		}
		if number, ok := value.Integer(); ok {
			limit := int64(9007199254740991)
			if kind == reflect.Float32 {
				limit = 16777215
			}
			if number < -limit || number > limit {
				return errAgentSettingsType
			}
			return nil
		}
	}
	return errAgentSettingsType
}
