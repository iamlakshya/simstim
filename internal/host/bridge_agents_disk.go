package host

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/supply"
)

func init() {
	if runtime.GOOS == "linux" && (runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64") {
		Features = append(Features, api.FeatureAgentsDiskV1)
	}
}

type agentsDiskService struct {
	mu       sync.Mutex
	closeMu  sync.Mutex
	active   sync.WaitGroup
	closed   bool
	receipts map[*InstalledAgentsReceiptV1]struct{}
}

// selected is the host's same-connection H5 consumer seam. A request cannot
// mint this receipt from inventory or a ContentRef: ensure must already have
// issued and retained the exact canonical producer receipt on this bridge.
func (s *agentsDiskService) selected(expected supply.ContentRef) (*InstalledAgentsReceiptV1, error) {
	s.mu.Lock()
	receipts := s.agentsReceiptsV1()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return nil, agentsBuildFailure("this authenticated connection has no selected agents producer receipt")
	}
	for _, receipt := range receipts {
		disk, err := receipt.Inventory()
		if err != nil || disk.Content != expected {
			continue
		}
		s.mu.Lock()
		_, retained := s.receipts[receipt]
		closed := s.closed
		s.mu.Unlock()
		if retained && !closed {
			return receipt, nil
		}
	}
	return nil, agentsBuildFailure("the outgoing client plan has no current selected agents producer receipt")
}

func (s *agentsDiskService) ensure(ctx context.Context) (image.AgentDiskV1, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return image.AgentDiskV1{}, agentsBuildFailure("the authenticated producer connection has ended")
	}
	s.active.Add(1)
	s.mu.Unlock()
	defer s.active.Done()
	if err := s.retryAgentsCleanupV1(); err != nil {
		return image.AgentDiskV1{}, err
	}
	receipt, err := EnsureInstalledAgentsV1(ctx)
	// Adopt the actual physical source before the error branch. A failed
	// factory can return a closed-for-use receipt whose cleanup must retry.
	s.retainAgentsReceiptV1(receipt)
	if err != nil {
		return image.AgentDiskV1{}, err
	}
	disk, err := receipt.Inventory()
	if err != nil {
		return disk, errors.Join(err, s.releaseAgentsReceiptV1(receipt))
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return disk, errors.Join(agentsBuildFailure("the producer connection ended before its inventory was sent"), s.releaseAgentsReceiptV1(receipt))
	}
	previous := s.agentsReceiptsV1()
	s.mu.Unlock()
	for _, other := range previous {
		if other == receipt {
			continue
		}
		// The native operations already issued both real objects. Prefer one
		// stable original token only to deduplicate retention; it grants no
		// selection or cleanup authority. Concurrent ensures cannot both close
		// their own receipt after observing each other.
		if existing, err := other.Inventory(); err == nil && existing.Content == disk.Content && other.publication.operation.token < receipt.publication.operation.token {
			return disk, s.releaseAgentsReceiptV1(receipt)
		}
	}
	// Keep each selected disk leased on THIS authenticated connection until
	// strong plan publication or connection end. Concurrent request plans may
	// have selected different actual installations; none evicts another's pin.
	return disk, nil
}

func (s *agentsDiskService) close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.active.Wait()
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	s.mu.Lock()
	receipts := s.agentsReceiptsV1()
	s.mu.Unlock()
	var result error
	for _, receipt := range receipts {
		result = errors.Join(result, s.closeAgentsReceiptV1(receipt))
	}
	return result
}

// All retained sources share this existing connection owner. The map keys
// are actual objects, not a content digest that would discard failed sources
// or replace a still-owned receipt for the same bytes.
func (s *agentsDiskService) agentsReceiptsV1() []*InstalledAgentsReceiptV1 {
	receipts := make([]*InstalledAgentsReceiptV1, 0, len(s.receipts))
	for receipt := range s.receipts {
		receipts = append(receipts, receipt)
	}
	return receipts
}
func (s *agentsDiskService) retainAgentsReceiptV1(receipt *InstalledAgentsReceiptV1) {
	if receipt == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.receipts == nil {
		s.receipts = map[*InstalledAgentsReceiptV1]struct{}{}
	}
	s.receipts[receipt] = struct{}{}
}
func (s *agentsDiskService) closeAgentsReceiptV1(receipt *InstalledAgentsReceiptV1) error {
	if err := receipt.Close(); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.receipts, receipt)
	s.mu.Unlock()
	return nil
}
func (s *agentsDiskService) releaseAgentsReceiptV1(receipt *InstalledAgentsReceiptV1) error {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	return s.closeAgentsReceiptV1(receipt)
}
func (s *agentsDiskService) retryAgentsCleanupV1() error {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	s.mu.Lock()
	receipts := s.agentsReceiptsV1()
	s.mu.Unlock()
	var result error
	for _, receipt := range receipts {
		receipt.mu.RLock()
		closed := receipt.closed
		receipt.mu.RUnlock()
		if closed {
			result = errors.Join(result, s.closeAgentsReceiptV1(receipt))
		}
	}
	return result
}

func (b *bridge) handleAgentsDisk(req proto.Request) bool {
	if req.Op != "agents.disk.ensure" {
		return false
	}
	if err := CheckCaller(); err != nil {
		b.reply(req.ID, nil, err)
		return true
	}
	fields, err := api.JSONObjectFields(req.Args, "version", "arch")
	var args api.AgentsDiskRequestV1
	if err != nil || len(fields) != 2 || json.Unmarshal(req.Args, &args) != nil || args.Version != 1 || args.Arch != runtime.GOARCH || runtime.GOOS != "linux" || args.Arch != "amd64" && args.Arch != "arm64" {
		b.reply(req.ID, nil, agentsBuildFailure("the closed agents disk request has an invalid version or host architecture"))
		return true
	}
	ctx := b.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	disk, err := b.agents.ensure(ctx)
	b.reply(req.ID, disk, agentsFailure(err))
	return true
}

func agentsFailure(err error) error {
	if err == nil {
		return nil
	}
	var typed *proto.Error
	if errors.As(err, &typed) {
		return err
	}
	return agentsBuildFailure(err.Error())
}
