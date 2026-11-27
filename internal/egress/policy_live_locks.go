package egress

import (
	"reflect"

	"github.com/iamlakshya/simstim/internal/api"
)

func projectLiveLocksV1(applied, desired []api.Lock) []api.Lock {
	old := map[api.LayerRef]api.Lock{}
	for _, lock := range applied {
		old[api.LayerRef{Kind: lock.Layer, Name: lock.Name}] = lock
	}
	var result []api.Lock
	for _, lock := range desired {
		key := api.LayerRef{Kind: lock.Layer, Name: lock.Name}
		previous := old[key]
		sandbox := previous.Sandbox
		sandbox.Preset, sandbox.Push = lock.Sandbox.Preset, lock.Sandbox.Push
		lock.Sandbox = sandbox
		result = append(result, lock)
		delete(old, key)
	}
	// Removing a nonlive lock takes effect at the next start too. Keep its
	// applied sandbox restriction while dropping its live network settings.
	for _, previous := range applied {
		key := api.LayerRef{Kind: previous.Layer, Name: previous.Name}
		if _, exists := old[key]; !exists {
			continue
		}
		sandbox := previous.Sandbox
		sandbox.Preset, sandbox.Push = nil, nil
		if reflect.DeepEqual(sandbox, api.SandboxValues{}) {
			continue
		}
		result = append(result, api.Lock{Layer: previous.Layer, Name: previous.Name, LayerValues: api.LayerValues{Sandbox: sandbox}})
	}
	return result
}
