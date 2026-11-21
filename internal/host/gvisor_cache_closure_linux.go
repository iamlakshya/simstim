//go:build linux

package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/jail"
	"github.com/iamlakshya/simstim/internal/supply"
	"golang.org/x/sys/unix"
)

// The digest is diagnostic reuse data, never an execution/namespace token.
// The caller separately owns the original runtime, native purpose and command.
func gvisorPhysicalKeyV1(ctx context.Context, manifest supply.Manifest, domain cachefs.Domain, source *jail.GvisorBootstrapV1) (string, error) {
	runtimeKey, err := selectedRuntimeKey(manifest)
	if err != nil {
		return "", err
	}
	bootstrap, err := source.IdentityV1(ctx)
	if err != nil {
		return "", err
	}
	boot := BootID()
	if boot == "" {
		return "", newRuntimeError("the native physical probe has no actual boot identity")
	}
	if err := domain.Validate(); err != nil {
		return "", err
	}
	var kernel unix.Utsname
	if err := unix.Uname(&kernel); err != nil {
		return "", err
	}
	return gvisorPhysicalDigestV1(runtimeKey, bootstrap, boot, domain, kernel)
}
func gvisorPhysicalDigestV1(runtimeKey, bootstrap, boot string, domain cachefs.Domain, kernel unix.Utsname) (string, error) {
	record := struct {
		Version                  int
		Runtime, Bootstrap, Boot string
		Domain                   cachefs.Domain
		Kernel                   unix.Utsname
	}{1, runtimeKey, bootstrap, boot, domain, kernel}
	data, err := json.Marshal(record)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

// validateGvisorCachePublicationV1 brackets publication with the real held
// producer. A copied OK row or old runtime-only proof cannot be published.
func validateGvisorCachePublicationV1(ctx context.Context, probes []api.WallProbe) error {
	for _, probe := range probes {
		if probe.Driver != "gvisor" || !probe.OK {
			continue
		}
		purpose := gvisorProbeFromContextV1(ctx)
		if purpose == nil {
			return newRuntimeError("physical gvisor facts have no original native producer")
		}
		if err := purpose.check(ctx); err != nil {
			return err
		}
		key, err := selectedRuntimeKey(purpose.selected.manifest)
		if err != nil || !cachedGvisorMatchesV1(probe, key, purpose.physicalKey) {
			return errors.Join(err, newRuntimeError("the physical cache row differs from the same held native closure"))
		}
	}
	return ctx.Err()
}

func requireCachedGvisorClosureV1(ctx context.Context, manifest supply.Manifest, domain cachefs.Domain, source *jail.GvisorBootstrapV1) error {
	closure, err := gvisorPhysicalKeyV1(ctx, manifest, domain, source)
	if err != nil {
		return err
	}
	runtimeKey, err := selectedRuntimeKey(manifest)
	if err != nil {
		return err
	}
	owner, err := factsPlacementOwnerV1(ctx)
	if err != nil {
		return err
	}
	if owner.domain() != domain {
		return newRuntimeError("the boot cache belongs to another actual host domain")
	}
	for _, cached := range cachedWallProbes(ctx, owner) {
		if cachedGvisorMatchesV1(cached, runtimeKey, closure) {
			actual, err := gvisorPhysicalKeyV1(ctx, manifest, domain, source)
			if err != nil {
				return err
			}
			if actual != closure {
				return newRuntimeError("the actual bootstrap changed during physical-proof lookup")
			}
			return nil
		}
	}
	return newRuntimeError("the actual boot has no physical proof for this same held C/helper/runtime closure")
}
