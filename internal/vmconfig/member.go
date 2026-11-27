package vmconfig

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"

	"github.com/iamlakshya/simstim/internal/proto"
)

// DecodeMember applies the same closed object barrier to the existing VM
// start/spawn DTO. It neither parses options nor changes the ordinary ABI.
func DecodeMember(data []byte) (proto.CtlStartArgs, error) {
	var args proto.CtlStartArgs
	kind := reflect.TypeOf(args)
	allowed := map[string]bool{}
	for i := 0; i < kind.NumField(); i++ {
		name := strings.Split(kind.Field(i).Tag.Get("json"), ",")[0]
		if name != "" && name != "-" {
			allowed[name] = true
		}
	}
	if _, err := fields(data, allowed); err != nil {
		return args, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil {
		return args, configError("the VM member data has an invalid field type")
	}
	return args, nil
}
