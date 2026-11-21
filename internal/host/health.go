package host

import (
	"context"
	"runtime"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/supply"
)

type hostHealthReadingsV1 struct {
	diskTotal    int64
	largestImage int64
	reboot       bool
	linger       bool
	lingerKnown  bool
	crashed      bool
	runtimes     []supply.Manifest
}

// appendHostHealthV1 observes diagnostics on the same existing helper domain.
// These rows never repair a cache, run a probe, reserve room or issue a unit.
func appendHostHealthV1(ctx context.Context, owner *placement, facts *api.HostFacts) {
	readings := hostHealthReadingsV1{}
	readings.diskTotal = hostHealthDiskTotalV1(paths.DataDir())
	readings.reboot, readings.linger, readings.lingerKnown = hostHealthNativeV1()
	store, err := image.OpenHostExisting(ctx, paths.ImagesDir(), owner.domain())
	if err == nil {
		if records, err := store.List(ctx); err == nil {
			for _, record := range records {
				if record.Arch == runtime.GOARCH {
					readings.largestImage = max(readings.largestImage, record.Size)
				}
			}
		}
		_ = store.Close()
	}
	observeRuntimeHealthV1(ctx, facts.BootID, facts.At, &readings)
	facts.Health = append(facts.Health, hostHealthItemsV1(*facts, readings)...)
}

func hostHealthItemsV1(facts api.HostFacts, readings hostHealthReadingsV1) []api.Health {
	items := []api.Health{}
	add := func(id, severity, message, hint string, levels ...string) {
		items = append(items, api.Health{ID: id, Severity: severity, Levels: levels, Message: message, Hint: hint})
	}
	if facts.DrainKnown && facts.Drain {
		add("drain", "blocks_new", "The host is draining and takes no new sandboxes.", "Clear the drain setting with host set drain=off when maintenance is complete.", "process", "gvisor", "microvm")
	} else if !facts.DrainKnown {
		add("drain-state", "blocks_new", "The host's current drain setting could not be checked.", "Keep the host state files and inspect the unfinished drain change before retrying.", "process", "gvisor", "microvm")
	}
	if readings.reboot {
		add("reboot", "warn", "The host reports that a restart is required.", "Drain the host, wait for sessions to finish, then restart it and clear the drain.")
	}
	if facts.DiskKnown {
		// Division avoids overflow for large filesystems or image sizes.
		switch {
		case readings.largestImage > 0 && facts.DiskFree < readings.largestImage:
			add("disk-low", "blocks_new", "Free disk is below the size of one cached image.", "Free disk space or remove unused images before starting another sandbox.", "process", "gvisor", "microvm")
		case readings.diskTotal > 0 && facts.DiskFree < readings.diskTotal/10 || readings.largestImage > 0 && facts.DiskFree/2 < readings.largestImage:
			add("disk-low", "warn", "Free disk is below ten per cent or twice the largest cached image.", "Free disk space or remove unused images before the host fills up.")
		}
	}
	if facts.PressureMemKnown && facts.PressureMem >= 10 {
		add("memory-pressure", "warn", "The host has sustained memory pressure of ten per cent or more.", "Stop an idle sandbox or add memory to the host.")
	}
	if readings.crashed {
		add("vm-crashed", "warn", "A virtual machine ended with a host error in the last 24 hours.", "Inspect that sandbox's supervisor log before restarting it.")
	}
	if readings.lingerKnown && !readings.linger {
		add("linger", "warn", "The user manager has linger turned off.", "Run host doctor --fix to keep sessions running after logout.")
	}
	if facts.OS == "darwin" && facts.OnBatteryKnown && facts.OnBattery {
		add("mac-on-battery", "warn", "The Mac host is running on battery power.", "Connect the host to mains power for long running sessions.")
	}
	eol, advisory := false, false
	for _, manifest := range readings.runtimes {
		manifest.Advisories = supply.MergeAdvisories(manifest.Advisories, supply.KnownBad())
		for _, item := range manifest.Items {
			eol = eol || item.Kind == supply.KindVMM && item.EOL != "" && item.EOL < facts.At.UTC().Format(time.DateOnly)
		}
		advisory = advisory || len(manifest.OwnFindings()) > 0
	}
	if eol {
		add("runtime-eol", "warn", "A running virtual machine uses a runtime whose fixes have ended.", "End the session when it is idle and start a new one with the current client.")
	}
	if advisory {
		add("advisory", "warn", "A running sandbox uses a runtime covered by a security advisory.", "End the session when it is idle and start a new one with the current client.")
	}
	return items
}

func observeRuntimeHealthV1(ctx context.Context, boot string, now time.Time, readings *hostHealthReadingsV1) {
	root, err := cachefs.OpenPrivateRoot(paths.SessionsDir(), false)
	if err != nil {
		return
	}
	defer root.Close()
	dir, err := root.Open(".")
	if err != nil {
		return
	}
	defer dir.Close()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if ctx.Err() != nil {
			return
		}
		if !validID(entry.Name()) || !entry.IsDir() {
			continue
		}
		manifest, err := readRuntimeReference(root, entry.Name())
		if err != nil || manifest.ID != entry.Name() || manifest.SandboxOf != "" || manifest.Wall == nil {
			continue
		}
		if recentVMCrashV1(&manifest, now) {
			readings.crashed = true
		}
		if manifest.Run.BootID != boot || manifest.RuntimeManifest == nil || manifest.Ended() {
			continue
		}
		unit, err := readMemoryUnitV1(ctx, manifest.ID, true)
		if err != nil || !unit.exists || declaredHealthUnitV1(&manifest, unit) != nil {
			continue
		}
		readings.runtimes = append(readings.runtimes, cloneSelectedRuntime(*manifest.RuntimeManifest))
	}
}

func recentVMCrashV1(manifest *Manifest, now time.Time) bool {
	if manifest == nil || manifest.Wall == nil || manifest.Wall.Level != "microvm" || manifest.Life == nil || manifest.Life.End == nil || manifest.Life.Phase != LifeEnded {
		return false
	}
	end := manifest.Life.End
	return end.Kind == EndSandbox && !end.Claimed && !end.At.IsZero() && !end.At.After(now) && now.Sub(end.At) <= 24*time.Hour
}

func declaredHealthUnitV1(manifest *Manifest, unit memoryUnitFactsV1) error {
	_, err := declaredUnitMemoryV1(manifest, unit)
	return err
}
