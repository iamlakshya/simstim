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
	s.jobs[a.Transfer] = &runtimeJob{ctx: ctx, cancel: cancel, requestID: requestID}
	return nil
}

func (s *runtimeService) fetch(ctx context.Context, a runtimecache.RuntimeRequest) (api.RuntimeStatus, error) {
	if err := s.registerFetch(ctx, a, 0); err != nil {
		return api.RuntimeStatus{}, err
	}
	return s.fetchRegistered(a, 0)
}

func (s *runtimeService) fetchRegistered(a runtimecache.RuntimeRequest, requestID uint64) (api.RuntimeStatus, error) {
	var result api.RuntimeStatus
	s.mu.Lock()
	job := s.jobs[a.Transfer]
	if job == nil || job.requestID != requestID || job.started {
		s.mu.Unlock()
		return result, fmt.Errorf("runtime fetch does not own a queued transfer")
	}
	job.started = true
	s.mu.Unlock()
	ctx := job.ctx
	defer func() {
		job.cancel()
		s.mu.Lock()
		delete(s.jobs, a.Transfer)
		delete(s.downloads, a.Transfer)
		s.mu.Unlock()
		<-runtimeSlots
	}()
	if err := ctx.Err(); err != nil {
		return result, err
	}

	store, err := runtimecache.Open(paths.ArtefactsDir())
	if err != nil {
		return result, err
	}
	defer store.Close()
	for _, item := range a.Manifest.Items {
		if err := store.Fetch(ctx, item, runtimecache.HTTPClient(), func(p *runtimecache.Part) {
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
	return s.status(ctx, a)
}

func (s *runtimeService) status(ctx context.Context, a runtimecache.RuntimeRequest) (api.RuntimeStatus, error) {
	result := api.RuntimeStatus{Runtime: a.Manifest.Runtime, Drivers: []api.RuntimeDriver{}, Items: []api.RuntimeFile{}, Transfers: []api.RuntimeTransfer{}}
	if err := runtimeValidateWithImageDefaults(a.Manifest, a.ImageDefaults); err != nil {
		return result, err
	}
	store, err := runtimecache.OpenReadOnly(paths.ArtefactsDir())
	if errors.Is(err, os.ErrNotExist) {
		for _, it := range a.Manifest.Items {
			result.Items = append(result.Items, api.RuntimeFile{Kind: it.Kind, Name: it.Name, SHA256: it.SHA256, Size: it.Size})
		}
		return result, nil
	}
	if err != nil {
		return result, err
	}
	defer store.Close()
	s.mu.Lock()
	active := len(s.parts)+len(s.jobs) > 0
	s.mu.Unlock()
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
	if !active && allPresent {
		result.Drivers = runtimeDrivers(a.Manifest)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, own := range s.parts {
		received, started := own.part.Progress()
		result.Transfers = append(result.Transfers, api.RuntimeTransfer{SHA256: own.digest, Received: received, Started: started})
	}
	for _, part := range s.downloads {
		received, started := part.Progress()
		result.Transfers = append(result.Transfers, api.RuntimeTransfer{SHA256: part.Digest(), Received: received, Started: started})
	}
	return result, nil
}
