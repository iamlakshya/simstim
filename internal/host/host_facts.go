package host

import (
	"context"
	"errors"
	"os"
	"runtime"
	"sort"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/runtimecache"
	"github.com/iamlakshya/simstim/internal/wall"
)

// HostFacts observes this actual helper's pinned host domain. It has no New
// request, SessionOperation, reservation, boot loan, runtime-selection receipt
// or callback that could acquire one. No probe, fetch, wake or process runs.
func HostFacts(ctx context.Context) (api.HostFacts, error) {
	facts := api.HostFacts{Version: api.HostFactsVersion, At: time.Now().UTC(), OS: runtime.GOOS, Arch: runtime.GOARCH,
		Walls: []api.WallProbe{}, RuntimeDigests: []string{}, ImageDigests: []string{}, Health: []api.Health{}}
	if ctx == nil {
		return facts, UserError("host_facts", "host facts have no request lifetime", "Request facts again through the current host connection.")
	}
	ctx, cancel := context.WithTimeout(ctx, capabilityTimeout)
	defer cancel()
	if err := errors.Join(CheckCaller(), ctx.Err()); err != nil {
		return facts, err
	}
	owner, err := factsPlacementOwnerV1(ctx)
	if err != nil {
		return facts, err
	}
	err = owner.withControl(ctx, func(control *placementControl) error {
		// Reuse BOTH existing native locks and the exact state-domain checks.
		// This observer deliberately never reconciles or writes idle/purge state.
		if err := owner.checkIdentity(); err != nil {
			return err
		}
		state, err := cachefs.OpenPrivateRoot(paths.StateDir(), false)
		if err != nil {
			return err
		}
		defer state.Close()
		lock, err := cachefs.OpenFile(state, "place.lock", os.O_RDWR, 0o600)
		if err != nil {
			return err
		}
		defer lock.Close()
		unlock, err := waitPlacementFile(ctx, state, "place.lock", lock, owner.placeRef)
		if err != nil {
			return err
		}
		defer unlock()
		if err := errors.Join(owner.checkIdentity(), owner.checkStateDomain(ctx, state, lock)); err != nil {
			return err
		}
		facts.HostID, facts.BootID = owner.hostID, BootID()
		facts.MemTotal = MemTotal()
		facts.MemForSessions = memoryMaxFor(facts.MemTotal)
		facts.MemoryKnown = facts.MemTotal > 0 && facts.MemForSessions > 0
		facts.DrainKnown = true
		facts.Drain = control.Drain != "" || control.ShutdownCommitted != nil && control.ShutdownCommitted.BootID == facts.BootID
		userDrain, drainErr := readUserDrainLockedV1(ctx, control, state)
		if drainErr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// A known purge/shutdown already proves the OR is on. Otherwise
			// missing native user-setting evidence must not become healthy off.
			if !facts.Drain {
				facts.DrainKnown = false
			}
		} else {
			facts.Drain = facts.Drain || userDrain.On
		}
		observed, accountingErr := observeMemoryRoomLockedV1(ctx, "", 0)
		facts.At = observed.At
		if ctx.Err() != nil {
			return ctx.Err()
		}
		applyHostMemoryFactsV1(&facts, observed, accountingErr)

		return owner.checkIdentity()
	}, false)
	if err != nil {
		return facts, err
	}
	probes := cachedWallProbes(ctx, owner)
	if len(probes) > 0 {
		projected, known := projectHostWallFactsV1(probes)
		facts.Walls, facts.WallsKnown, facts.LevelKnown = projected, known, known
		best := wall.None
		for _, probe := range projected {
			level, err := wall.ParseLevel(probe.Level)
			if err == nil && probe.OK && level > best {
				best = level
			}
		}
		facts.Level = best.String()
	}
	facts.DiskFree, facts.DiskKnown = hostFactsDiskV1(paths.DataDir())
	facts.PressureMem, facts.PressureMemKnown = hostFactsPressureV1("memory")
	facts.PressureCPU, facts.PressureCPUKnown = hostFactsPressureV1("cpu")
	facts.OnBattery, facts.OnBatteryKnown = hostFactsBatteryV1()
	facts.RuntimeDigests, facts.RuntimeDigestsKnown = hostFactsRuntimeDigestsV1(ctx, owner)
	facts.ImageDigests, facts.ImageDigestsKnown = hostFactsImageDigestsV1(ctx, owner)
	appendHostHealthV1(ctx, owner, &facts)
	appendSlotCapacityFactsV1(ctx, &facts)
	if err := errors.Join(ctx.Err(), owner.checkIdentity()); err != nil {
		return facts, err
	}
	trimHostMemoryObservationFrameV1(&facts)
	return facts, nil
}

func hostFactsRuntimeDigestsV1(ctx context.Context, owner *placement) ([]string, bool) {
	result := []string{}
	store, err := runtimecache.OpenHostReadOnly(ctx, paths.ArtefactsDir(), owner.domain())
	if errors.Is(err, os.ErrNotExist) {
		return result, true
	}
	if err != nil {
		return result, false
	}
	defer store.Close()
	records, err := store.RecordsContext(ctx, runtime.GOARCH)
	if err != nil {
		return result, false
	}
	seen := map[string]bool{}
	for _, record := range records {
		for _, item := range record.Manifest.Items {
			present, err := store.Present(ctx, item)
			if err != nil {
				return []string{}, false
			}
			// This is explicitly the existing cheap native size/mode presence claim,
			// never a byte-verification or runtime-execution readiness capability.
			if present && !seen[item.SHA256] {
				seen[item.SHA256] = true
				result = append(result, item.SHA256)
			}
		}
	}
	sort.Strings(result)
	return result, true
}

func hostFactsImageDigestsV1(ctx context.Context, owner *placement) ([]string, bool) {
	result := []string{}
	store, err := image.OpenHostExisting(ctx, paths.ImagesDir(), owner.domain())
	if errors.Is(err, os.ErrNotExist) {
		return result, true
	}
	if err != nil {
		return result, false
	}
	defer store.Close()
	records, err := store.List(ctx)
	if err != nil {
		return result, false
	}
	seen := map[string]bool{}
	for _, record := range records {
		if record.Arch != runtime.GOARCH {
			continue
		}
		for _, digest := range []string{record.Digest, record.PlatformDigest} {
			if image.ValidDigest(digest) && !seen[digest] {
				seen[digest] = true
				result = append(result, digest)
			}
		}
	}
	sort.Strings(result)
	return result, true
}

// Partial or mismatched observations remain unknown. A zero on an unknown
// read can never become selectable free capacity or an empty reservation sum.
func applyHostMemoryFactsV1(facts *api.HostFacts, observed memoryRoomFactsV1, accountingErr error) {
	facts.AccountingKnown = accountingErr == nil && observed.Known && facts.MemoryKnown && observed.BootID == facts.BootID && !observed.At.IsZero() && observed.Budget == facts.MemForSessions && observed.Reserved >= 0 && observed.Room == observed.Budget-observed.Reserved && observed.Sandboxes >= 0
	if facts.AccountingKnown {
		facts.MemReserved, facts.Room, facts.Sandboxes = observed.Reserved, observed.Room, observed.Sandboxes
		if api.ValidateMemoryObservationV1(observed.Observation, facts) == nil {
			facts.MemoryObservation = api.CloneMemoryObservationV1(observed.Observation)
		}
		return
	}
	facts.MemoryObservation = nil
	facts.MemReserved, facts.Room, facts.Sandboxes = 0, 0, 0
	facts.Health = append(facts.Health, api.Health{ID: "memory_accounting", Severity: "blocks_new", Levels: []string{"process", "gvisor", "microvm"}, Message: "Current native memory accounting is unavailable.", Hint: "Run host doctor and inspect the current canonical unit reservations before selecting this host."})
}

// Project display diagnostics through the existing text contract, preserving
// every execution-relevant identity and readiness bit. These are metadata
// copies; the original native cache remains untouched for actual New.
func projectHostWallFactsV1(probes []api.WallProbe) ([]api.WallProbe, bool) {
	result := make([]api.WallProbe, 0, len(probes))
	for _, probe := range probes {
		if _, err := wall.ParseLevel(probe.Level); err != nil || probe.Driver == "" || api.CleanText(probe.Driver, 1024) != probe.Driver {
			return []api.WallProbe{}, false
		}
		projected := probe
		projected.Reason, projected.Fix = api.CleanText(probe.Reason, 1024), api.CleanText(probe.Fix, 1024)
		if probe.Facts != nil {
			projected.Facts = make(map[string]string, len(probe.Facts))
			for key, value := range probe.Facts {
				projected.Facts[key] = value
			}
		}
		result = append(result, projected)
	}
	return result, true
}

// Facts require the genuine normal entry already established by main. Do not
// call placementForCall here: its initialisation adapter can create a host ID,
// root or guard when no entry exists. A diagnostic cannot acquire that role.
func factsPlacementOwnerV1(ctx context.Context) (*placement, error) {
	scope, _, err := placementScope()
	if err != nil {
		return nil, err
	}
	for !placementProcesses.TryLock() {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Millisecond):
		}
	}
	defer placementProcesses.Unlock()
	owner := placementProcesses.entry
	if owner == nil || owner.installer || owner.scope != scope || !samePersistentRootBinding(owner.boundRoots, currentPersistentRoots()) {
		return nil, UserError("host_facts", "the facts request has no original normal helper placement owner", "Request facts through the current admitted host helper connection.")
	}
	return owner, nil
}
