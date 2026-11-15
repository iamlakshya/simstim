package vmconfig

import (
	"bytes"
	"encoding/json"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// Decode is the one closed VMPrepare codec shared by producer and init. It
// rejects duplicates and case aliases before the ordinary typed validator.
func Decode(data []byte) (Plan, error) {
	outer, err := fields(data, map[string]bool{"session": true, "vm": true, "state_files": true, "capture_nonce": true, "correlation": true})
	if err != nil {
		return Plan{}, err
	}
	if _, err := fields(outer["vm"], map[string]bool{"version": true, "system": true, "sudo": true, "docker": true, "state_dirs": true, "restore_capture_nonce": true}); err != nil {
		return Plan{}, err
	}
	if raw,present:=outer["correlation"];present {
		values,err:=fields(raw,map[string]bool{"v":true,"session":true,"generation":true,"need_sha256":true,"nonce":true})
		if err!=nil || len(values)!=5{return Plan{},configError("the original VM correlation is not an exact supported object")}
	}
	var args proto.CtlVMPrepareArgs
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil {
		return Plan{}, configError("the VM prepare data has an invalid field type")
	}
	return Validate(args)
}

func fields(data []byte, allowed map[string]bool) (map[string]json.RawMessage, error) {
	keys := make([]string, 0, len(allowed))
	for key := range allowed {
		keys = append(keys, key)
	}
	values, err := api.JSONObjectFields(data, keys...)
	if err != nil {
		return nil, configError("the VM data is not one unambiguous JSON object")
	}
	for key := range values {
		if !allowed[key] {
			return nil, configError("the VM data has an unknown field")
		}
	}
	return values, nil
}
