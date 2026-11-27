package configdata

import (
	"errors"
	"reflect"

	"github.com/iamlakshya/simstim/internal/api"
)

// ErrConfigTools means a file requested session-only model capability data.
// It contains no field value, tool name, command or decoded source content.
var ErrConfigTools = errors.New("tools require a session model grant")

// CheckConfigTools is the ONE full/FD file application guard. Call only on
// an actual final known table and exact-before-Unicode-fold schema spelling.
// Unrelated unknown/free-map keys keep the canonical decoder's old policy.
// JSON trusted session/manifest Rule DTOs are outside this TOML file guard.
func CheckConfigTools(typ reflect.Type, hasTools bool) error {
	if typ == nil || !hasTools {
		return nil
	}
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	switch typ {
	case reflect.TypeFor[api.NetworkValues](), reflect.TypeFor[api.NetworkRule](), reflect.TypeFor[api.Rule]():
		return ErrConfigTools
	default:
		return nil
	}
}
