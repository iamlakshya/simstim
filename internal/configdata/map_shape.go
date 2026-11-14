package configdata

import (
	"encoding"
	"errors"
	"reflect"

	"github.com/BurntSushi/toml"
)

// ErrConfigMapShape means a present known map has a non-table final value.
// It deliberately carries no source value, which may contain a secret.
var ErrConfigMapShape = errors.New("must be a table")

// CheckMapShape checks one known field's final canonical container shape.
// Call it only for a present value, after pinned grammar resolution, not a
// transient parser mutation. Unknown fields and absent maps are not checked.
// Native TOML and text unmarshallers retain responsibility for their input.
func CheckMapShape(typ reflect.Type, isFinalTable bool) error {
	if typ == nil || isFinalTable {
		return nil
	}
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Map {
		return nil
	}
	tomlDecoder := reflect.TypeFor[toml.Unmarshaler]()
	textDecoder := reflect.TypeFor[encoding.TextUnmarshaler]()
	for _, candidate := range []reflect.Type{typ, reflect.PointerTo(typ)} {
		if candidate.Implements(tomlDecoder) || candidate.Implements(textDecoder) {
			return nil
		}
	}
	return ErrConfigMapShape
}
