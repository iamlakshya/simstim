package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/paths"
)

// InstalledAgentsReceiptV1 is minted only by the canonical installed-agent
// producer. Its fields, active publication and native shared lease are private.
// Inventory, ContentRef, guest data and a zero value cannot mint this receipt.
type InstalledAgentsReceiptV1 struct {
	mu          sync.RWMutex
	once        sync.Once
	closeMu     sync.Mutex
	finished    bool
	disk        image.AgentDiskV1
	lease       *image.Lease
	store       *image.Store
	publication *runtimePublication
	done        chan struct{}
	closed      bool
	closeErr    error
}

// EnsureInstalledAgentsV1 builds from the canonical table and pinned host
// scope. The caller closes its receipt after exact metadata publication/use.
// A nonnil receipt on error retains the SAME failed cleanup owner. The caller
// must adopt it before handling the error and retry Close until it succeeds.
func EnsureInstalledAgentsV1(ctx context.Context) (receipt *InstalledAgentsReceiptV1, returned error) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		return nil, agentsBuildFailure("this host does not support the Linux agents disk converter")
	}
	publication, err := beginRuntimePublication(ctx)
	if err != nil {
		return nil, err
	}
	return ensureInstalledAgentsPublicationV1(publication)
}

func ensureInstalledAgentsPublicationV1(publication *runtimePublication) (receipt *InstalledAgentsReceiptV1, returned error) {
	if publication == nil {
		return nil, agentsBuildFailure("the installed agents producer has no original native publication")
	}
	receipt = &InstalledAgentsReceiptV1{publication: publication, done: make(chan struct{})}
	owned := receipt
	defer func() {
		if returned != nil {
			if publication.ctx.Err() != nil {
				returned = errors.Join(returned, context.Cause(publication.ctx))
			}
			finishAgentsFactoryV1(&receipt, &returned)
		}
	}()
	login, err := LoginEnvContext(publication.ctx, 15*time.Second)
	if err != nil {
		return receipt, agentsBuildFailure("the canonical login environment is unavailable: " + err.Error())
	}
	// placementScope validated this exact canonical HOME before creating its
	// control path. Never use a remote field or recompute ambient HOME here.
	home := filepath.Dir(publication.owner.control)
	search := envValue(login, "PATH")
	receipt.store, err = image.OpenHost(publication.ctx, paths.ImagesDir(), publication.owner.domain(), publication.gate)
	if err != nil {
		return receipt, err
	}
	receipt.disk, receipt.lease, err = receipt.store.EnsureAgentsV1(publication.ctx, runtime.GOARCH, func(ctx context.Context, previous string, output *os.File) (image.AgentsBuildResultV1, error) {
		built, err := buildAgentsScoped(ctx, agentsBuildPlanV1{Version: 1, Arch: runtime.GOARCH, Home: home, SearchPath: search, PreviousSource: previous}, output)
		return image.AgentsBuildResultV1{SourceDigest: built.SourceDigest, Agents: built.Agents, Reused: built.Reused}, err
	})
	if err != nil {
		return receipt, err
	}
	go func() {
		select {
		case <-publication.ctx.Done():
			_ = owned.Close()
		case <-owned.done:
		}
	}()
	return receipt, nil
}

// finishAgentsFactoryV1 transfers the SAME native object on cleanup error.
// It never turns an error into an inventory or creates a replacement owner.
func finishAgentsFactoryV1(receipt **InstalledAgentsReceiptV1, returned *error) {
	if *returned == nil || *receipt == nil {
		return
	}
	cleanup := (*receipt).Close()
	*returned = errors.Join(*returned, cleanup)
	if cleanup == nil {
		*receipt = nil
	}
}

// Inventory returns an independent outside-G11 producer description. It does
// not reveal the private lease or authorise a protected-plan replacement.
func (r *InstalledAgentsReceiptV1) Inventory() (image.AgentDiskV1, error) {
	if r == nil {
		return image.AgentDiskV1{}, agentsBuildFailure("the installed agents receipt is missing")
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed || r.publication == nil || r.lease == nil || r.store == nil || r.done == nil {
		return image.AgentDiskV1{}, agentsBuildFailure("the installed agents receipt is closed or was not produced here")
	}
	if r.publication.ctx.Err() != nil {
		return image.AgentDiskV1{}, context.Cause(r.publication.ctx)
	}
	disk := r.disk
	disk.Agents = slices.Clone(disk.Agents)
	return disk, disk.Validate(runtime.GOARCH)
}

// WithPublicationV1 retains the private producer lease while the canonical
// protected writer commits. The callback runs under the SAME short native
// placement gate; it must not enter a second gate or perform RPC/conversion.
func (r *InstalledAgentsReceiptV1) WithPublicationV1(ctx context.Context, publish func(image.AgentDiskV1) error) error {
	if r == nil || publish == nil {
		return agentsBuildFailure("the agents publication has no canonical receipt or writer")
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed || r.publication == nil || r.lease == nil || r.store == nil || r.done == nil {
		return agentsBuildFailure("the installed agents publication receipt is no longer live")
	}
	disk := r.disk
	disk.Agents = slices.Clone(disk.Agents)
	return r.publication.gate(ctx, func() error { return publish(disk) })
}

// Close releases native disk/store capabilities before ending the original
// active operation. Cancellation and connection end share this one cleanup.
func (r *InstalledAgentsReceiptV1) Close() error {
	if r == nil {
		return nil
	}
	r.once.Do(func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.closed = true
	})
	r.closeMu.Lock()
	defer r.closeMu.Unlock()
	if r.finished {
		return nil
	}
	if r.lease != nil {
		if err := r.lease.Close(); err != nil {
			r.closeErr = err
			return err
		}
	}
	if r.store != nil {
		if err := r.store.Close(); err != nil {
			r.closeErr = err
			return err
		}
	}
	if r.publication != nil {
		if err := r.publication.close(); err != nil {
			r.closeErr = err
			return err
		}
	}
	if r.done != nil {
		close(r.done)
	}
	r.finished = true
	r.closeErr = nil
	return nil
}
