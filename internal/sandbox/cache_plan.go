package sandbox

import (
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/proto"
)

// CacheStatePlan adds the already merged cache selection to an owned per-start
// state plan. Legacy records without either cache field use built-in defaults.
// An explicit merged empty list stays empty; it never refills dropped paths.
func CacheStatePlan(base files.StatePlan, values *api.SandboxValues) (files.StatePlan, error) {
	enabled := true
	var selected []string
	if values == nil || values.KeepCaches == nil && values.Caches == nil {
		var err error
		selected, err = DefaultPaths()
		if err != nil {
			return files.StatePlan{}, &proto.Error{Code: "bad_value", Message: "built-in sandbox cache data cannot be used: " + err.Error(), Hint: "Update the host helper, then run host doctor."}
		}
	} else {
		selected = values.Caches
		if values.KeepCaches != nil {
			enabled = *values.KeepCaches
		}
	}
	plan, err := AppendCacheDirs(base, enabled, selected)
	if err != nil {
		return files.StatePlan{}, &proto.Error{Code: "bad_value", Message: "sandbox cache state plan cannot be used: " + err.Error(), Hint: "Run config check, then choose cache folders outside agent state, programs, settings and credentials."}
	}
	return plan, nil
}
