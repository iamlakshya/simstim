package host

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"runtime"
	"sync"

	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/runtimecache"
	"github.com/iamlakshya/simstim/internal/supply"
)

const runtimeProcessTransfers = 16

var runtimeSlots = make(chan struct{}, runtimeProcessTransfers)

type runtimeOwnedPart struct {
	part        *runtimecache.Part
	store       *runtimecache.Store
	digest      string
	publication *runtimePublication
}
type runtimeJob struct {
	ctx       context.Context
	cancel    context.CancelFunc
	requestID uint64
	started   bool
}

type runtimeService struct {
	mu         sync.Mutex
	parts      map[string]*runtimeOwnedPart
	jobs       map[string]*runtimeJob
	downloads  map[string]*runtimecache.Part
	closed     bool
	selections map[string]*selectedRuntimeReceiptV1
}

func (s *runtimeService) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	for _, job := range s.jobs {
		job.cancel()
	}
	for token, part := range s.parts {
		_ = part.close()
		delete(s.parts, token)
		<-runtimeSlots
	}
	for key, receipt := range s.selections {
		_ = receipt.Close()
		delete(s.selections, key)
	}
}

func runtimeValidate(m supply.Manifest) error { return runtimeValidateWithImageDefaults(m, nil) }

func runtimeValidateWithImageDefaults(m supply.Manifest, defaults *[]supply.Item) error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("runtime installation needs a Linux host")
	}
	return runtimecache.ValidateWithImageDefaults(m, defaults, runtime.GOARCH)
}

func (s *runtimeService) reserve() error {
	if s.closed {
		return fmt.Errorf("runtime connection is closed")
	}
	if len(s.parts)+len(s.jobs) >= runtimecache.MaxTransfers {
		return UserError("runtime_busy", "this connection has reached its runtime transfer limit", "Wait for one transfer to finish, then try again.")
	}
	select {
	case runtimeSlots <- struct{}{}:
		return nil
	default:
		return UserError("runtime_busy", "this helper process has reached its runtime transfer limit", "Wait for a transfer in this process to finish, then try again.")
	}
}

func (s *runtimeService) begin(a runtimecache.RuntimeInstall) (runtimecache.RuntimePart, error) {
	return s.beginContext(context.Background(), a)
}

func (s *runtimeService) beginContext(ctx context.Context, a runtimecache.RuntimeInstall) (runtimecache.RuntimePart, error) {
	var result runtimecache.RuntimePart
	if err := CheckCaller(); err != nil {
		return result, err
	}
	if a.ImageDefaults != nil {
		return result, fmt.Errorf("image defaults are accepted only at runtime commit")
	}
	if a.Manifest == nil {
		return result, fmt.Errorf("runtime begin has no client manifest")
	}
	if err := runtimeValidate(*a.Manifest); err != nil {
		return result, err
	}
	var item supply.Item
	for _, it := range a.Manifest.Items {
		if it.SHA256 == a.SHA256 {
			item = it
			break
		}
	}
	if item.SHA256 == "" {
		return result, fmt.Errorf("runtime file is not named by the client's manifest")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.reserve(); err != nil {
		return result, err
	}
	release := true
	defer func() {
		if release {
			<-runtimeSlots
		}
	}()
	publication, store, err := openRuntimeProductionStore(ctx)
	if err != nil {
		return result, err
	}
	part, err := store.BeginContext(publication.ctx, item)
	if err != nil {
		store.Close()
		_ = publication.close()
		return result, err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		part.Close()
		store.Close()
		_ = publication.close()
		return result, fmt.Errorf("name runtime transfer: %w", err)
	}
	result.Part = hex.EncodeToString(nonce[:])
	if s.parts == nil {
		s.parts = map[string]*runtimeOwnedPart{}
	}
	s.parts[result.Part] = &runtimeOwnedPart{part: part, store: store, digest: item.SHA256, publication: publication}
	release = false
	return result, nil
}

func (s *runtimeService) install(ctx context.Context, a runtimecache.RuntimeInstall) (any, error) {
	if err := CheckCaller(); err != nil {
		return nil, err
	}
	switch a.Action {
	case "begin":
		return s.beginContext(ctx, a)
	case "commit":
		if a.Manifest == nil {
			return nil, fmt.Errorf("runtime commit has no client manifest")
		}
		if err := runtimeValidatePublication(*a.Manifest, a.ImageDefaults); err != nil {
			return nil, err
		}
		publication, store, err := openRuntimeProductionStore(ctx)
		if err != nil {
			return nil, err
		}
		defer publication.close()
		defer store.Close()
		if err := commitRuntime(ctx, store, *a.Manifest, a.ImageDefaults); err != nil {
			return nil, err
		}
		return nil, s.rememberRuntime(ctx, *a.Manifest)
	case "write", "finish", "abort":
		s.mu.Lock()
		if a.Action == "abort" {
			if job := s.jobs[a.Part]; job != nil {
				job.cancel()
				s.mu.Unlock()
				return nil, nil
			}
		}
		own := s.parts[a.Part]
		if own == nil {
			s.mu.Unlock()
			return nil, fmt.Errorf("runtime part does not belong to this connection")
		}
		if a.Action == "write" {
			err := own.part.WriteAt(a.Data, a.Offset)
			if err != nil {
				delete(s.parts, a.Part)
				_ = own.close()
				<-runtimeSlots
			}
			s.mu.Unlock()
			return nil, err
		}
		delete(s.parts, a.Part)
		s.mu.Unlock()
		defer func() { _ = own.close(); <-runtimeSlots }()
		if a.Action == "finish" {
			return nil, own.part.Finish(ctx)
		}
		return nil, nil
	default:
		return nil, fmt.Errorf("unknown runtime installation phase")
	}
}

func runtimeFailure(err error) error {
	if err == nil {
		return nil
	}
	var user *proto.Error
	if errors.As(err, &user) {
		return err
	}
	var admission *runtimecache.AdmissionError
	if errors.As(err, &admission) {
		return UserError("runtime_space", admission.Error(), "Free disk space on the host, then run host upgrade again.")
	}
	return UserError("runtime_install", "could not install the runtime: "+err.Error(), "Check the host's free disk space and runtime cache permissions, then run host upgrade again.")
}
