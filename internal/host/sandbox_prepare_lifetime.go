package host

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// This counter only prevents process shutdown from dropping an existing
// original request worker. It grants no operation or cleanup authority.
var liveSandboxPrepareRequestsV1 atomic.Int64

type sandboxPrepareResponseV1 struct {
	bridge *bridge
	id     uint64
	once   sync.Once
}

func newSandboxPrepareResponseV1(b *bridge, id uint64) *sandboxPrepareResponseV1 {
	return &sandboxPrepareResponseV1{bridge: b, id: id}
}

func (response *sandboxPrepareResponseV1) reply(result api.SandboxPrepareResultV1, err error) {
	response.once.Do(func() { response.bridge.reply(response.id, result, err) })
}

// Registration occurs on the original bridge dispatch loop, before its
// existing request goroutine starts. Shutdown therefore cannot miss a queued
// prepare worker. The same worker removes its mark after actual cleanup.
func (b *bridge) trackSandboxPrepareRequestV1(req proto.Request) func() {
	if req.Op != "sandbox.prepare" {
		return nil
	}
	b.prepares.Add(1)
	liveSandboxPrepareRequestsV1.Add(1)
	return func() {
		liveSandboxPrepareRequestsV1.Add(-1)
		b.prepares.Done()
	}
}

func waitSandboxPrepareRequestsV1() {
	for liveSandboxPrepareRequestsV1.Load() != 0 {
		time.Sleep(20 * time.Millisecond)
	}
}

func (b *bridge) closeOwnedServicesV1(cancel context.CancelFunc, input OwnedBridgeInput, output OwnedBridgeOutput) error {
	cancel()
	// Cancellation revokes construction. The original prepare request
	// still owns cleanup and the runtime/agent/image receipts it uses.
	b.prepares.Wait()
	err := errors.Join(b.closeRewindPreparationsV1(), b.closeResumePreparationsV1(), b.closeImageSavesV1(), b.agents.close(), b.images.close(), b.runtime.close())
	if input != nil {
		_ = input.Cancel()
	}
	if output != nil {
		_ = output.Cancel()
	}
	b.closePull()
	b.closeCopy()
	return err
}
