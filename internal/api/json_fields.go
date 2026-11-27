package api

import (
	"context"
	"encoding/json"
	"errors"
)

// ErrJSONObject marks an invalid, duplicated or aliased JSON object.
var ErrJSONObject = errors.New("invalid, duplicated or aliased JSON object")

// JSONObjectFields is the shared object barrier used by local grant state
// and policy pins. Unknown values keep their original bytes.
func JSONObjectFields(raw []byte, known ...string) (map[string]json.RawMessage, error) {
	return ProjectedJSONObjectFields(context.Background(), raw, known...)
}
