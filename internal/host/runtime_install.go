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
	mu          sync.Mutex
	closing     bool
	slot        bool
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
	done      chan struct{}
	finished  sync.Once
	released  sync.Once
	cleanupMu sync.Mutex
	resource  *runtimeOwnedPart
}

type runtimeService struct {
	active           sync.WaitGroup
	closeMu          sync.Mutex
	owned            map[*runtimeOwnedPart]struct{}
	selectionGate    chan struct{}
	selectionPending *selectedRuntimeReceiptV1
	mu               sync.Mutex
	parts            map[string]*runtimeOwnedPart
	jobs             map[string]*runtimeJob
	downloads        map[string]*runtimecache.Part
	closed           bool
	closeDone        chan struct{}
	selections       map[string]*selectedRuntimeReceiptV1
}

func (s *runtimeService) close() error {
	return s.closeRuntimeServiceV1()
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
	resources := len(s.owned)
	for _, owner := range s.parts {
		if _, held := s.owned[owner]; !held {
			resources++
		}
	}
	if resources+len(s.jobs) >= runtimecache.MaxTransfers {
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
	done, enterErr := s.enterRuntimeV1()
	if enterErr != nil {
		return result, enterErr
	}
	defer done()
	if err := s.retryRuntimeOwnersV1(); err != nil {
		return result, err
	}
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
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return result, fmt.Errorf("name runtime transfer: %w", err)
	}
	owner, err := s.newRuntimeOwnerV1()
	if err != nil {
		return result, err
	}
	owner.digest = item.SHA256
	owner.publication, owner.store, err = openRuntimeProductionStore(ctx)
	if err != nil {
		return result, errors.Join(err, s.finishRuntimeOwnerV1(owner))
	}
	owner.part, err = owner.store.BeginContext(owner.publication.ctx, item)
	if err != nil {
		return result, errors.Join(err, s.finishRuntimeOwnerV1(owner))
	}
	result.Part = hex.EncodeToString(nonce[:])
	s.mu.Lock()
	if s.parts == nil {
		s.parts = map[string]*runtimeOwnedPart{}
	}
	s.parts[result.Part] = owner
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return result, errors.Join(fmt.Errorf("runtime connection ended before begin acknowledgement"), s.finishRuntimeOwnerV1(owner))
	}
	return result, nil
}

func (s *runtimeService) install(ctx context.Context, a runtimecache.RuntimeInstall) (any, error) {
	if err := CheckCaller(); err != nil {
		return nil, err
	}
	if a.Action == "begin" {
		return s.beginContext(ctx, a)
	}
	done, enterErr := s.enterRuntimeV1()
	if enterErr != nil {
		return nil, enterErr
	}
	defer done()
	switch a.Action {
	case "commit":
		if a.Manifest == nil {
			return nil, fmt.Errorf("runtime commit has no client manifest")
		}
		if err := runtimeValidatePublication(*a.Manifest, a.ImageDefaults); err != nil {
			return nil, err
		}
		if err := s.retryRuntimeOwnersV1(); err != nil {
			return nil, err
		}
		return nil, s.commitRuntimeOwnedV1(ctx, *a.Manifest, a.ImageDefaults)
	case "write", "finish", "abort":
		if a.Action == "abort" {
			if found, err := s.cancelRuntimeJobCleanupV1(a.Part); found {
				return nil, err
			}
		}
		s.mu.Lock()
		owner := s.parts[a.Part]
		s.mu.Unlock()
		if owner == nil {
			return nil, fmt.Errorf("runtime part does not belong to this connection")
		}
		owner.mu.Lock()
		if owner.closing {
			owner.mu.Unlock()
			cleanup := s.finishRuntimeOwnerV1(owner)
			if a.Action == "abort" {
				return nil, cleanup
			}
			return nil, errors.Join(fmt.Errorf("runtime transfer is closing; this operation was not accepted"), cleanup)
		}
		var operationErr error
		if a.Action == "write" {
			operationErr = owner.part.WriteAt(a.Data, a.Offset)
		}
		if a.Action == "finish" {
			operationErr = owner.part.Finish(ctx)
		}
		owner.mu.Unlock()
		if a.Action == "write" && operationErr == nil {
			return nil, nil
		}
		return nil, errors.Join(operationErr, s.finishRuntimeOwnerV1(owner))
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
