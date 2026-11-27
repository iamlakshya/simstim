package host

import (
	"context"
	"fmt"
	"runtime"
	"sort"

	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/runtimecache"
	"github.com/iamlakshya/simstim/internal/supply"
)

// afterRuntimeImageLeases lets retention tests pause before publication.
// Production leaves it nil. It never changes the lease or authority rules.
var afterRuntimeImageLeases func()

// commitRuntime publishes a label while holding all client-named default
// platform image leases. Image collection rereads defaults under its exclusive
// lease. The order is sorted image leases before the runtime transaction.
func commitRuntime(ctx context.Context, store *runtimecache.Store, manifest supply.Manifest, defaults *[]supply.Item) (returned error) {
	if err := runtimeValidatePublication(manifest, defaults); err != nil {
		return err
	}
	if len(*defaults) == 0 {
		return store.CommitWithImageDefaults(ctx, manifest, defaults, runtime.GOARCH)
	}
	images, err := image.Open(paths.ImagesDir())
	if err != nil {
		return fmt.Errorf("open image retention leases: %w", err)
	}
	defer func() {
		if err := images.Close(); returned == nil && err != nil {
			returned = fmt.Errorf("close image retention store: %w", err)
		}
	}()
	seen := map[string]bool{}
	var digests []string
	for _, item := range *defaults {
		if !seen[item.Digest] {
			seen[item.Digest] = true
			digests = append(digests, item.Digest)
		}
	}
	sort.Strings(digests)
	var leases []*image.Lease
	defer func() {
		for i := len(leases) - 1; i >= 0; i-- {
			if err := leases[i].Close(); returned == nil && err != nil {
				returned = fmt.Errorf("close image retention lease: %w", err)
			}
		}
	}()
	for _, digest := range digests {
		lease, err := images.Acquire(ctx, digest)
		if err != nil {
			return fmt.Errorf("hold default image retention lease: %w", err)
		}
		leases = append(leases, lease)
	}
	if afterRuntimeImageLeases != nil {
		afterRuntimeImageLeases()
	}
	return store.CommitWithImageDefaults(ctx, manifest, defaults, runtime.GOARCH)
}

// runtimeValidatePublication refuses unknown image authority before production
// label publication. New clients always send explicit empty or populated data.
func runtimeValidatePublication(manifest supply.Manifest, defaults *[]supply.Item) error {
	if defaults == nil {
		return UserError("runtime_image_metadata", "runtime image-default metadata needs a client refresh", "Update the client and run host upgrade again.")
	}
	if *defaults == nil {
		return UserError("runtime_image_metadata", "runtime image-default metadata must be an explicit array", "Update the client and run host upgrade again.")
	}
	if err := runtimeValidateWithImageDefaults(manifest, defaults); err != nil {
		return fmt.Errorf("validate runtime publication: %w", err)
	}
	return nil
}
