package supervisor

import (
	"context"

	"github.com/iamlakshya/simstim/internal/host"
)

// publishNativeAccountReadyV1 runs only after the actual native VM and guest
// certified readiness, just before the original durable ready acknowledgement.
// It reuses the same journal and generation, with no metadata-to-source path.
func (s *Supervisor) publishNativeAccountReadyV1(ctx context.Context) error {
	if ctx == nil {
		return execRefusal("the account display has no original owner context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	selected := s.m.Wall != nil && s.m.Wall.Level == "microvm" && (s.m.Wall.Driver == "chv" || s.m.Wall.Driver == "firecracker" || s.m.Wall.Driver == "nitro")
	runtime, ok := s.sb.(vmRuntime)
	connection := s.vmStartConnection
	generation := s.generation
	live := s.primary == nil && s.rec.phase == host.LifeRunning && s.m.Run.Generation == generation && generation != "" && !s.stopIntent
	s.mu.Unlock()
	if !selected {
		return nil
	}
	receipt := host.OriginalVMNeedFromContext(ctx)
	if !ok || runtime.Sandbox == nil || !live || receipt == nil || connection == nil {
		return execRefusal("the account display has no same original native ready run")
	}
	own, err := receipt.OriginalAccountModeV1(ctx, runtime.Sandbox, connection)
	if err != nil {
		return err
	}
	return s.commitNativeAccountDisplayV1(ctx, generation, own)
}

// commitNativeAccountDisplayV1 serialises only descriptive journal data after
// the private caller has checked the actual original native ready issuer.
func (s *Supervisor) commitNativeAccountDisplayV1(ctx context.Context, generation string, own bool) error {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	retired := false
	var snapshot host.Manifest
	err := s.writeManifestChange(&snapshot, false, nil, nil, func(target *host.Manifest) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		s.mu.Lock()
		live := s.nativeAccountDisplayLiveLockedV1(generation)
		s.mu.Unlock()
		if target.Run.Generation != generation || s.generation != generation {
			return execRefusal("the native account generation changed before its descriptive publication")
		}
		target.Run.NativeAccountMode, target.Run.NativeAccountGeneration = "", ""
		if own && live && target.Life != nil && target.Life.Phase == host.LifeRunning && !target.Life.Started.IsZero() && target.Life.Started.Equal(target.Run.StartedAt) && target.Run.BootID == host.BootID() {
			target.Run.NativeAccountMode = host.NativeAccountOwnV1
			target.Run.NativeAccountGeneration = generation
		}
		return nil
	}, func(saved *host.Manifest) {
		// The canonical writer holds mu here, after durable IO. A stop or
		// phase change during Save must never revive a positive live mark.
		retired = ctx.Err() != nil || !s.nativeAccountDisplayLiveLockedV1(generation)
		if retired {
			s.m.Run.NativeAccountMode, s.m.Run.NativeAccountGeneration = "", ""
			return
		}
		s.m.Run.NativeAccountMode, s.m.Run.NativeAccountGeneration = saved.Run.NativeAccountMode, saved.Run.NativeAccountGeneration
	})
	if err != nil {
		return err
	}
	s.mu.Lock()
	retired = retired || ctx.Err() != nil || !s.nativeAccountDisplayLiveLockedV1(generation)
	if retired {
		s.m.Run.NativeAccountMode, s.m.Run.NativeAccountGeneration = "", ""
	}
	s.mu.Unlock()
	if !retired {
		return nil
	}
	// A phase change can occur while disk IO runs outside mu. Still under
	// the SAME journal turn, save a fresh retired snapshot before returning.
	// This never holds mu across IO or trusts the earlier native observation.
	var stopped host.Manifest
	err = s.writeManifestChange(&stopped, false, nil, nil, func(target *host.Manifest) error {
		target.Run.NativeAccountMode, target.Run.NativeAccountGeneration = "", ""
		return nil
	}, func(*host.Manifest) {
		s.m.Run.NativeAccountMode, s.m.Run.NativeAccountGeneration = "", ""
	})
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return errStopped
}

// nativeAccountDisplayLiveLockedV1 is checked both before prospective edit
// and after durable IO. Its caller holds mu, never across disk operations.
func (s *Supervisor) nativeAccountDisplayLiveLockedV1(generation string) bool {
	return s.m != nil && s.primary == nil && !s.stopIntent && s.rec.phase == host.LifeRunning &&
		generation != "" && s.generation == generation && s.m.Run.Generation == generation &&
		s.m.Run.BootID == host.BootID() && s.m.Wall != nil && s.m.Wall.Level == "microvm" &&
		(s.m.Wall.Driver == "chv" || s.m.Wall.Driver == "firecracker" || s.m.Wall.Driver == "nitro")
}

// retireNativeAccountV1 runs on every canonical snapshot, so old queued saves
// cannot revive a previous generation, a failed boot or a stopped run's label.
func (s *Supervisor) retireNativeAccountV1(manifest *host.Manifest) {
	if s.stopIntent || s.rec.phase != host.LifeRunning || manifest.Wall == nil || manifest.Wall.Level != "microvm" || manifest.Wall.Driver != "chv" && manifest.Wall.Driver != "firecracker" && manifest.Wall.Driver != "nitro" || manifest.Run.BootID != host.BootID() || s.generation == "" || manifest.Run.Generation != s.generation || manifest.Run.NativeAccountMode != host.NativeAccountOwnV1 || manifest.Run.NativeAccountGeneration != s.generation {
		manifest.Run.NativeAccountMode, manifest.Run.NativeAccountGeneration = "", ""
	}
}
