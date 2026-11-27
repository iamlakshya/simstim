package host

import (
	"context"
	"encoding/json"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/paths"
	"os"
	"runtime"
	"sync"
)

type wallCache struct {
	BootID  string          `json:"boot_id"`
	Version string          `json:"version"`
	Walls   []api.WallProbe `json:"walls"`
}

func writeWallCache(probes []api.WallProbe) error {
	return writeWallCacheContext(context.Background(), probes)
}

func writeWallCacheContext(ctx context.Context, probes []api.WallProbe) error {
	if err := CheckCaller(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return stateMetadataFailure(err)
	}
	if err := validateGvisorCachePublicationV1(ctx, probes); err != nil {
		return err
	}
	helperVersion := helperWallVersion()
	if helperVersion == "" {
		return UserError("state_metadata", "cannot identify the helper for the sandbox probe cache", "Inspect the running helper before refreshing its sandbox probe cache.")
	}
	data, err := json.Marshal(wallCache{BootID: BootID(), Version: helperVersion, Walls: probes})
	if err != nil {
		return stateMetadataFailure(err)
	}
	if err := validateGvisorCachePublicationV1(ctx, probes); err != nil {
		return err
	}
	return publishStateMetadata(ctx, stateWalls, data)
}

// cachedWallProbes is for session starts.
// sandbox.probe deliberately never reads it. An unknown boot has no cache.
func cachedWallProbes(ctx context.Context, owner *placement) []api.WallProbe {
	boot := BootID()
	helperVersion := helperWallVersion()
	if boot == "" || helperVersion == "" || owner == nil {
		return nil
	}
	var probes []api.WallProbe
	err := owner.withControl(ctx, func(control *placementControl) error {
		if err := owner.checkIdentity(); err != nil {
			return err
		}
		state, err := cachefs.OpenPrivateRoot(paths.StateDir(), false)
		if err != nil {
			return err
		}
		defer state.Close()
		if err := checkStateMetadataRoot(control, state); err != nil {
			return err
		}
		data, info, err := readStateMetadata(ctx, state, stateWalls)
		if err != nil {
			return err
		}
		expected, recorded := control.Refs["state/"+string(stateWalls)]
		if !recorded || info == nil || !expected.matches(info) || len(expected.extra) != 0 {
			return stateMetadataPreserved(stateWalls, "its cached facts have no exact native producer receipt")
		}
		if _, pending := control.StateWrites[string(stateWalls)]; pending {
			return stateMetadataPreserved(stateWalls, "its actual publication is still uncertain")
		}
		probes = wallProbesFor(data, boot, helperVersion)
		return owner.checkIdentity()
	}, false)
	if err != nil {
		return nil
	}
	return probes
}

func wallProbesFor(data []byte, boot, helperVersion string) []api.WallProbe {
	if boot == "" || helperVersion == "" {
		return nil
	}
	var cache wallCache
	if validateStateMetadata(stateWalls, data) != nil || json.Unmarshal(data, &cache) != nil || cache.BootID != boot || cache.Version != helperVersion {
		return nil
	}
	return cache.Walls
}

var helperWallVersionOnce = sync.OnceValue(func() string {
	if runtime.GOOS == "linux" {
		return executableWallVersion("/proc/self/exe")
	}
	name, err := os.Executable()
	if err != nil {
		return ""
	}
	return executableWallVersion(name)
})

func helperWallVersion() string { return helperWallVersionOnce() }
