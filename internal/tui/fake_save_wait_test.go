package tui

import "context"

// Count only the actual cancellable wait. Active CAS and result processing
// remain visible to the harness after this gate releases.
func (b *fakeBackend) waitSaveGate(ctx context.Context) error {
	if b.saveGate == nil {
		return nil
	}
	b.blocked.Add(1)
	defer b.blocked.Add(-1)
	if b.saving != nil {
		close(b.saving)
	}
	select {
	case <-b.saveGate:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// A completed CAS cannot be cancelled back into an unsaved host. Tests may
// hold its still-owned result to check that Close joins the same transaction.
func (b *fakeBackend) waitCommittedSaveResult() {
	if b.committedSaveGate == nil {
		return
	}
	b.blocked.Add(1)
	defer b.blocked.Add(-1)
	<-b.committedSaveGate
}
