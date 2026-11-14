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
	active   sync.WaitGroup
	closed   bool
	receipts map[string]*InstalledAgentsReceiptV1
}

// selected is the host's same-connection H5 consumer seam. A request cannot
// mint this receipt from inventory or a ContentRef: ensure must already have
// issued and retained the exact canonical producer receipt on this bridge.
func (s *agentsDiskService) selected(expected supply.ContentRef) (*InstalledAgentsReceiptV1, error) {
	s.mu.Lock()
	receipt := s.receipts[expected.SHA256]
	closed := s.closed
	s.mu.Unlock()
	if closed || receipt == nil {
		return nil, agentsBuildFailure("this authenticated connection has no selected agents producer receipt")
	}
	disk, err := receipt.Inventory()
	if err != nil {
		return nil, err
	}
	if disk.Content != expected {
		return nil, agentsBuildFailure("the outgoing client plan differs from its selected agents producer receipt")
	}
	return receipt, nil
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
	receipt, err := EnsureInstalledAgentsV1(ctx)
	if err != nil {
		return image.AgentDiskV1{}, err
	}
	disk, err := receipt.Inventory()
	if err != nil {
		return disk, errors.Join(err, receipt.Close())
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return disk, errors.Join(agentsBuildFailure("the producer connection ended before its inventory was sent"), receipt.Close())
	}
	if s.receipts == nil {
		s.receipts = map[string]*InstalledAgentsReceiptV1{}
	}
	if previous := s.receipts[disk.Content.SHA256]; previous != nil {
		if _, err := previous.Inventory(); err == nil {
			s.mu.Unlock()
			return disk, receipt.Close()
		}
	}
	s.receipts[disk.Content.SHA256] = receipt
	s.mu.Unlock()
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
	s.mu.Lock()
	receipts := s.receipts
	s.receipts = nil
	s.mu.Unlock()
	var result error
	for _, receipt := range receipts {
		result = errors.Join(result, receipt.Close())
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
