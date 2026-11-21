package supervisor

import (
	"context"
	"errors"
	"github.com/iamlakshya/simstim/internal/host"
	"time"
)

// prepareManagerCustodyV1 runs on the actual initial-ready path before the
// original outside grant is released. No supervisor mutex is held across a
// manager query, and a temporary prepare boot never acquires this lifetime.
func (s *Supervisor) prepareManagerCustodyV1() error {
	if s.primary != nil || s.bootOwner == nil || s.isIdlePrepareBootV1() {
		return nil
	}
	s.mu.Lock()
	plain := s.m.Wall != nil && s.m.Wall.Driver == driverPlain
	existing := s.custody
	s.mu.Unlock()
	if plain || existing != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(s.lifetimeContext(), systemGrace)
	defer cancel()
	owner, err := s.bootOwner.PrepareManagerCustodyV1(ctx)
	s.mu.Lock()
	s.custody = owner
	s.mu.Unlock()
	return err
}

// depositManagerCustodyV1 is an actual shutdown caller, after the original
// runtime waiter and cleanup have joined. Kernel cgroup checks independently
// reject stray writers; an intent bit cannot certify quiescence.
func (s *Supervisor) depositManagerCustodyV1() {
	s.mu.Lock()
	owner := s.custody
	cleanupErr := errors.Join(s.runtimeCleanupErr, s.noneCleanupCommitErr)
	runtime := s.sb
	attempted := s.sandboxStartAttempted
	s.mu.Unlock()
	if owner == nil {
		return
	}
	if cleanupErr != nil || attempted && s.checkBackendPhysicalClosureV1(runtime) != nil {
		s.log.Error("native backend closure is incomplete; retaining original manager custody main", "err", cleanupErr)
		select {}
	}
	if s.al != nil {
		// finish has closed admission for new alerts. Join the actual loop
		// and delivery owners before the cgroup's self-only custody check.
		if err := s.al.Join(context.Background()); err != nil {
			s.log.Error("alert owners did not join before native custody; retaining the original main", "err", err)
			select {}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), systemGrace)
	err := owner.DepositAfterQuiescenceV1(ctx)
	cancel()
	if err == nil {
		return
	}
	s.log.Error("native manager custody did not settle; retaining original main and native copies", "err", err)
	// Unknown deposit/ACK cannot release the original source. This actor owns
	// the still-fenced main, rather than manufacturing a stopped row or cold
	// source. An exact future recovery must use its actual retained objects.
	for {
		time.Sleep(time.Minute)
	}
}

func (s *Supervisor) closeManagerCustodyV1() error {
	s.mu.Lock()
	owner := s.custody
	s.mu.Unlock()
	if owner == nil {
		return nil
	}
	reported := false
	for {
		unknown := owner.Close()
		if unknown == nil {
			break
		}
		if !reported {
			s.log.Error("native custody retirement is uncertain; retaining the original main and copies", "err", unknown)
			reported = true
		}
		// This original main owns the actual pending resources. Caller
		// cancellation cannot turn unknown native retirement into closure.
		time.Sleep(250 * time.Millisecond)
	}
	if diagnostic := owner.CloseDiagnosticsV4(); diagnostic != nil {
		s.log.Warn("native custody retired with an original I/O outcome", "err", diagnostic)
	}
	s.mu.Lock()
	if s.custody == owner {
		s.custody = nil
	}
	s.mu.Unlock()
	return nil
}

// An original manager main may escrow only after every actual backend owner
// has positively joined. Self-only in its user cgroup does not cover a slot
// keeper, privileged VMM or gofer admitted outside that unit.
func (s *Supervisor) checkBackendPhysicalClosureV1(runtime sandbox) error {
	physical, ok := runtime.(interface{ PhysicalExitV1() bool })
	if ok && physical.PhysicalExitV1() {
		return nil
	}
	s.mu.Lock()
	managed := s.custody != nil
	s.mu.Unlock()
	if !ok && !managed {
		return nil
	}
	return host.UserError("backend_closure", "the original backend still has live or uncertain native owners", "Retain the original supervisor and its resources until its native backend owners have joined.")
}
