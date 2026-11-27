// Package gvisor defines the pinned userspace-kernel runtime and its driver.
package gvisor

import (
	"fmt"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/runtimecache"
	"github.com/iamlakshya/simstim/internal/supply"
	"path"
	"runtime"
)

var requiredRoles = []string{"runsc", "gvisor_sentry", "gvisor-sentry-prewarmer", "gvisor-proc-umounter", "runsc-fd-parking"}

func runtimeRoot() string { return path.Join("/opt", brand.Name, "runtime", "gvisor") }

// RuntimeTargetV1 is the fixed target of one signed executable role. This
// path is only layout data; its original checked descriptor grants the mount.
func RuntimeTargetV1(name string) string {
	if name == "runsc" {
		return path.Join(runtimeRoot(), "runsc")
	}
	return path.Join(runtimeRoot(), "gvisor-bin", name)
}

func runtimeItems(manifest supply.Manifest) ([]supply.Item, error) {
	return RuntimeItemsV1(manifest, runtime.GOARCH)
}

// RuntimeItemsV1 selects the exact same-build executable closure from the
// client's own signed manifest. This pure selection grants no launch authority.
func RuntimeItemsV1(manifest supply.Manifest, arch string) ([]supply.Item, error) {
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	main, ok := manifest.Find(supply.KindGvisor, "runsc", arch)
	if !ok || supply.Below(main.Version, "release-20261102.0") {
		return nil, failure("runtime_missing", "the client runtime lacks its pinned runsc release", nil)
	}
	if main.Version != NativeBuildVersionV1 || main.Released != "2026-11-02" {
		return nil, failure("runtime_missing", "the client runtime lacks the supported patched gvisor companion build", nil)
	}
	var items []supply.Item
	for _, name := range requiredRoles {
		item, ok := manifest.Find(supply.KindGvisor, name, arch)
		if !ok {
			return nil, failure("runtime_missing", "the client runtime lacks required gvisor companion "+name, nil)
		}
		if item.Version != main.Version || item.Released != main.Released {
			return nil, failure("runtime_missing", "gvisor executables must come from the same signed build and release", nil)
		}
		items = append(items, item)
	}
	selected := manifest
	selected.Items = items
	if err := runtimecache.Validate(selected, arch); err != nil {
		return nil, err
	}
	if findings := selected.OwnFindings(); len(findings) != 0 {
		return nil, failure("runtime_advisory", "gvisor runtime has a known advisory", fmt.Errorf("%s", findings[0].String()))
	}
	return items, nil
}
