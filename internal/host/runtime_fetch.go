package host

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/runtimecache"
)

// registerFetch runs in the bridge reader before asynchronous dispatch. An
// abort that follows the request can therefore always cancel the owned job.
func (s *runtimeService) registerFetch(ctx context.Context, a runtimecache.RuntimeRequest, requestID uint64) error {
	if err := CheckCaller(); err != nil {
		return err
	}
	if err := runtimeValidatePublication(a.Manifest, a.ImageDefaults); err != nil {
		return err
	}
	if len(a.Transfer) != 32 || strings.Trim(a.Transfer, "0123456789abcdef") != "" {
		return fmt.Errorf("runtime fetch needs a transfer identifier")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.jobs[a.Transfer] != nil {
		return fmt.Errorf("runtime transfer identifier is already in use")
	}
	if err := s.reserve(); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	if s.jobs == nil {
		s.jobs = map[string]*runtimeJob{}
	}
	s.jobs[a.Transfer] = &runtimeJob{ctx: ctx, cancel: cancel, requestID: requestID, done: make(chan struct{})}
	return nil
}

func (s *runtimeService) fetch(ctx context.Context, a runtimecache.RuntimeRequest) (api.RuntimeStatus, error) {
	if err := s.registerFetch(ctx, a, 0); err != nil {
		return api.RuntimeStatus{}, err
	}
	return s.fetchRegistered(a, 0)
}

func (s *runtimeService) fetchRegistered(a runtimecache.RuntimeRequest, requestID uint64) (result api.RuntimeStatus, returned error) {
	s.mu.Lock()
	job := s.jobs[a.Transfer]
	if job == nil || job.requestID != requestID || job.started {
		s.mu.Unlock()
		return result, fmt.Errorf("runtime fetch does not own a queued transfer")
	}
	job.started = true
	s.mu.Unlock()
	ctx := job.ctx
	defer func() { returned = errors.Join(returned, s.finishRuntimeJobV1(a.Transfer, job)) }()
	if err := ctx.Err(); err != nil {
		return result, err
	}

	if err := s.retryRuntimeOwnersV1(); err != nil {
		return result, err
	}
	if err := s.retryRuntimeSelectionAdmissionV1(ctx); err != nil {
		return result, err
	}
	owner := &runtimeOwnedPart{}
	s.mu.Lock()
	job.resource = owner
	s.mu.Unlock()
	publication, store, err := openRuntimeProductionStore(ctx)
	owner.publication, owner.store = publication, store
	if err != nil {
		return result, err
	}
	for _, item := range a.Manifest.Items {
		if err := store.Fetch(ctx, item, runtimecache.HTTPClient(), func(p *runtimecache.Part) {
			owner.mu.Lock()
			owner.part = p
			owner.mu.Unlock()
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.downloads == nil {
				s.downloads = map[string]*runtimecache.Part{}
			}
			s.downloads[a.Transfer] = p
		}); err != nil {
			return result, err
		}
	}
	if err := commitRuntime(ctx, store, a.Manifest, a.ImageDefaults); err != nil {
		return result, err
	}
	// Commit checked every original named blob and its real durability. Close
	// that SAME delivery owner before issuing the connection-held receipt.
	if err := owner.close(); err != nil {
		return result, err
	}
	if err := s.rememberRuntimeWithDefaults(ctx, a.Manifest, a.ImageDefaults); err != nil {
		return result, err
	}
	result = runtimeStatusV1(a)
	for _, it := range a.Manifest.Items {
		result.Items = append(result.Items, api.RuntimeFile{Kind: it.Kind, Name: it.Name, SHA256: it.SHA256, Size: it.Size, Present: true})
	}
	result.Drivers = runtimeDrivers(a.Manifest)
	return result, nil
}

func runtimeStatusV1(a runtimecache.RuntimeRequest) api.RuntimeStatus {
	return api.RuntimeStatus{Runtime: a.Manifest.Runtime, Drivers: []api.RuntimeDriver{}, Items: []api.RuntimeFile{}, Transfers: []api.RuntimeTransfer{}}
}

func (s *runtimeService) status(ctx context.Context, a runtimecache.RuntimeRequest) (result api.RuntimeStatus, returned error) {
	done, err := s.enterRuntimeV1()
	if err != nil {
		return api.RuntimeStatus{}, err
	}
	defer done()
	result = runtimeStatusV1(a)
	_, request, release, err := s.selectionContextV1(ctx)
	if err != nil {
		return result, err
	}
	defer release()
	ctx = request
	if err := s.retryRuntimeOwnersV1(); err != nil {
		return result, err
	}
	if err := s.retryRuntimeSelectionAdmissionV1(ctx); err != nil {
		return result, err
	}
	if err := runtimeValidateWithImageDefaults(a.Manifest, a.ImageDefaults); err != nil {
		return result, err
	}
	owner, err := placementForCall()
	if err != nil {
		return result, err
	}
	inventory, err := s.newRuntimeInventoryOwnerV1()
	if err != nil {
		return result, err
	}
	defer func() { returned = errors.Join(returned, s.finishRuntimeOwnerV1(inventory)) }()
	store, err := runtimecache.OpenHostReadOnly(ctx, paths.ArtefactsDir(), owner.domain())
	inventory.store = store
	if errors.Is(err, os.ErrNotExist) {
		for _, it := range a.Manifest.Items {
			result.Items = append(result.Items, api.RuntimeFile{Kind: it.Kind, Name: it.Name, SHA256: it.SHA256, Size: it.Size})
		}
		return result, nil
	}
	if err != nil {
		return result, err
	}
	active := s.runtimeWritersActiveV1()
	allPresent := true
	for _, it := range a.Manifest.Items {
		var present bool
		var err error
		if active {
			present, err = store.Present(ctx, it)
		} else {
			present, err = store.Has(ctx, it)
		}
		if err != nil {
			return result, err
		}
		allPresent = allPresent && present
		result.Items = append(result.Items, api.RuntimeFile{Kind: it.Kind, Name: it.Name, SHA256: it.SHA256, Size: it.Size, Present: present})
	}
	// No successful selection can precede a failed inventory close. Failure
	// retains this exact owner/slot; the next admission retries it first.
	if err := s.finishRuntimeOwnerV1(inventory); err != nil {
		return result, err
	}
	if !active && allPresent {
		if err := s.rememberRuntimeWithDefaults(ctx, a.Manifest, a.ImageDefaults); err != nil {
			return result, err
		}
		result.Drivers = runtimeDrivers(a.Manifest)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, own := range s.parts {
		own.mu.Lock()
		if own.part == nil {
			own.mu.Unlock()
			continue
		}
		received, started := own.part.Progress()
		own.mu.Unlock()
		result.Transfers = append(result.Transfers, api.RuntimeTransfer{SHA256: own.digest, Received: received, Started: started})
	}
	for _, part := range s.downloads {
		received, started := part.Progress()
		result.Transfers = append(result.Transfers, api.RuntimeTransfer{SHA256: part.Digest(), Received: received, Started: started})
	}
	return result, nil
}
