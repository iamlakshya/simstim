package supervisor

import (
	"context"
	"errors"
	"net"
	"sync"

	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/wall"
)

// This private adapter retains only actual objects returned by a failed
// constructor. It cannot start, publish readiness or issue native authority.
// The genuinely accepted prepare supervisor separately owns destruction.
type failedSandboxPreparationV1 struct {
	mu      sync.Mutex
	runtime wall.Sandbox
	capture *files.Capture
	disk    *diskResourcesV1
}

func (failed *failedSandboxPreparationV1) Start(context.Context) (net.Conn, error) {
	return nil, execRefusal("the failed preparation owns cleanup only and cannot start")
}

func (failed *failedSandboxPreparationV1) Kill() error {
	if failed.runtime != nil {
		return failed.runtime.Kill()
	}
	return nil
}

func (failed *failedSandboxPreparationV1) Wait() error {
	if failed.runtime != nil {
		return failed.runtime.Wait().Err
	}
	return nil
}

func (failed *failedSandboxPreparationV1) ownedDiskResourcesV1() *diskResourcesV1 { return failed.disk }

func (failed *failedSandboxPreparationV1) Destroy() error {
	failed.mu.Lock()
	defer failed.mu.Unlock()
	if failed.runtime != nil {
		if err := failed.runtime.Destroy(); err != nil {
			return err // Retry the SAME concrete actual runtime on uncertainty.
		}
	}
	var cleanup error
	if failed.disk != nil {
		cleanup = errors.Join(cleanup, failed.disk.Close())
	}
	if failed.capture != nil {
		cleanup = errors.Join(cleanup, failed.capture.Close())
	}
	// These SAME lower owners must retain their objects and errors on a
	// failed Close. Their retryable producer corrections are a dependency;
	// a later no-op nil cannot replace genuine physical cleanup.
	return cleanup
}

// Ordinary unstarted sessions release prepared handles without destroying
// saved session data. This preserves their existing cleanup semantics.
func (failed *failedSandboxPreparationV1) releasePreparedV1() error {
	failed.mu.Lock()
	defer failed.mu.Unlock()
	var cleanup error
	if failed.runtime != nil {
		release, ok := failed.runtime.(interface{ ReleasePreparedV1() error })
		if !ok {
			return execRefusal("the failed original runtime has no non-destructive prepared-resource release")
		}
		cleanup = release.ReleasePreparedV1()
	}
	if failed.disk != nil {
		cleanup = errors.Join(cleanup, failed.disk.Close())
	}
	if failed.capture != nil {
		cleanup = errors.Join(cleanup, failed.capture.Close())
	}
	return cleanup
}
