package host

import (
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/iamlakshya/simstim/internal/wall"
)

// runtimeCompatibility is an optional contract for a complete registered
// driver. A future driver must check its installed manifest entries before
// returning a verified compatibility result. Downloaded files alone never
// satisfy this contract and diagnostic-only probes cannot register drivers.
type runtimeCompatibility interface {
	RuntimeCompatibility(supply.Manifest) (api.RuntimeDriver, bool)
}

func runtimeDrivers(manifest supply.Manifest) []api.RuntimeDriver {
	drivers := []api.RuntimeDriver{}
	for _, driver := range wall.Drivers() {
		checked, ok := driver.(runtimeCompatibility)
		if !ok {
			continue
		}
		result, ok := checked.RuntimeCompatibility(manifest)
		if ok && result.Name == driver.Name() {
			drivers = append(drivers, result)
		}
	}
	return drivers
}
