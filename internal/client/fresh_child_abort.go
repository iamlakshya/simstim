package client

import "context"

// This removes only the creator's exact never-attempted LOCAL reservation.
// It sends no host operation and proves no remote absence after a wire attempt.
func (p *freshChildPlan) abort(ctx context.Context) (bool, error) {
	p.mu.Lock()
	if p.retired {
		p.mu.Unlock()
		return true, nil
	}
	if p.busy {
		p.mu.Unlock()
		return false, agentModeResourceError()
	}
	if p.attempted || p.published || p.creation == nil {
		p.mu.Unlock()
		return false, nil
	}
	original := cloneSandboxPolicy(p.creation)
	p.mu.Unlock()
	retired, err := retireUnpublishedSandboxContext(ctx, p.conn, original, p)
	if retired {
		p.mu.Lock()
		p.retired, p.aborting = true, true
		p.mu.Unlock()
	}
	return retired, err
}

func (p *freshChildPlan) holdUnpublishedSandboxRetirement(ctx context.Context, c *Conn, original *SandboxPolicy) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	if p.closed || p.busy || p.attempted || p.published || p.retired || p.conn != c ||
		p.creation == nil || !sameSandboxCreation(p.creation, original) || p.creation.Ceiling != original.Ceiling {
		p.mu.Unlock()
		return nil, sandboxReceiptError()
	}
	// Terminal revocation survives a failed lower cleanup. A later caller
	// may retry that cleanup, but no new command or first writer can proceed.
	p.aborting = true
	return p.mu.Unlock, nil
}
