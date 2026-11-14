package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"
)

// RememberSetupContext keeps the hash-bound answer under the accepted trust
// locks. Waiting and pre-commit persistence stop when ctx ends. A committed
// answer stays committed even when cancellation follows its rename.
func RememberSetupContext(ctx context.Context, host, repo string, run bool, hash string) (warn *UserError, err error) {
	if err := ctx.Err(); err != nil {
		return nil, setupTrustStopped(err)
	}
	deadline := time.Now().Add(trustWait)
	if err := lockTrustProcess(ctx, deadline); err != nil {
		return nil, setupTrustWaitError(err)
	}
	defer trustMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, setupTrustStopped(err)
	}
	if err := os.MkdirAll(StateDir(), 0o700); err != nil {
		return nil, fmt.Errorf("make %s: %w", StateDir(), err)
	}
	remaining := max(time.Until(deadline), 0)
	unlock, err := lockPathContext(ctx, trustPath()+".lock", remaining, true)
	if err != nil {
		return nil, setupTrustWaitError(err)
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return nil, setupTrustStopped(err)
	}
	f, warn, stuck, err := loadTrustContext(ctx)
	if err != nil {
		return warn, setupTrustStopped(err)
	}
	if stuck {
		return nil, warn
	}
	return warn, saveAnswerContext(ctx, f, host, repo, run, hash)
}

func lockTrustProcess(ctx context.Context, deadline time.Time) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if trustMu.TryLock() {
			if err := ctx.Err(); err != nil {
				trustMu.Unlock()
				return err
			}
			return nil
		}
		if !time.Now().Before(deadline) {
			return errLockBusy
		}
		timer := time.NewTimer(time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func setupTrustWaitError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return setupTrustStopped(err)
	}
	return trustLockError("the answer is not kept", err)
}

func setupTrustStopped(err error) error {
	ue := &UserError{Msg: "cancelled before the setup answer was kept", Hint: "Run the command again.", Code: "cancelled", Kind: KindCancelled}
	if errors.Is(err, context.DeadlineExceeded) {
		ue = &UserError{Msg: "timed out before the setup answer was kept", Hint: "Run the command again.", Code: "timeout"}
	}
	return &StoppedError{UserError: ue, Err: err}
}
