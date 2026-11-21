package client

import (
	"context"
	"reflect"
	"slices"
)

// H5 calls this at each actual New preparation/value intake, before borrowing
// settings, selecting names or fetching values. A nil options pointer is for
// APIs without NewInputOptions. On the option-taking path an omitted CWD is
// filled only from the selection's already-owned original CWD.
func checkHostAttemptIntakeV1(ctx context.Context, actual NewArgs, options *NewInputOptions) error {
	if ctx == nil {
		return grantOperationError()
	}
	r := grantRequestOf(ctx)
	if r == nil {
		return nil // existing callers retain their own ordinary request checks
	}
	r.mu.Lock()
	a := r.hostAttempt
	r.mu.Unlock()
	if a == nil {
		return nil
	}
	if a.plan == nil || a.request != r {
		return grantOperationError()
	}
	p := a.plan
	p.mu.Lock()
	a.mu.Lock()
	valid := p.active == a && !p.closed && p.selection != nil && p.ctx != nil &&
		a.ctx != nil && a.ready && !a.closing && !a.closed && !a.started
	originalContext := a.ctx
	a.mu.Unlock()
	p.mu.Unlock()
	if !valid {
		return grantOperationError()
	}
	if err := originalContext.Err(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	inputs := r.newInputs
	valid = r.hostAttempt == a && r.Untrusted == p.need.Untrusted && r.SSHAgent == p.args.SSHAgent &&
		r.purpose == grantRemote && r.operation == grantOpNew && !r.ending && !r.preparationClosed
	r.mu.Unlock()
	if !valid || !sameHostAttemptCoreIntentV1(p.args, actual) {
		return grantOperationError()
	}
	if inputs == nil && (len(actual.Layers) != 0 || actual.Inputs != nil ||
		actual.Environment != nil || actual.RepositoryPolicy != nil) {
		return grantOperationError()
	}
	if options != nil {
		p.selection.mu.Lock()
		valid = !p.selection.closed && p.selection.ready && p.selection.ctx != nil && p.selection.ctx.Err() == nil
		expected := cloneNewInputOptions(p.selection.options)
		p.selection.mu.Unlock()
		if !valid {
			return grantOperationError()
		}
		provided := cloneNewInputOptions(*options)
		if provided.OriginalCWD == "" {
			provided.OriginalCWD = expected.OriginalCWD
		}
		if !reflect.DeepEqual(provided, expected) {
			return grantOperationError()
		}
		*options = provided
	}
	// Before a preparation step, original auto/default fields are allowed as
	// well as the held source's already-applied defaults. A sealed secret
	// plan is checked by its existing arguments/selection owners below.
	if actual.Inline == p.args.Inline && actual.MemoryMax == p.args.MemoryMax {
		return nil
	}
	return a.checkOriginalIntentV1(ctx, actual)
}

func sameHostAttemptCoreIntentV1(original, actual NewArgs) bool {
	return actual.Agent == original.Agent && slices.Equal(actual.Argv, original.Argv) &&
		actual.Name == original.Name && actual.Cwd == original.Cwd && actual.Base == original.Base &&
		actual.Worktree == original.Worktree && actual.Task == original.Task &&
		actual.Downgrade == original.Downgrade && actual.SSHAgent == original.SSHAgent &&
		(!original.MakeFolder || actual.MakeFolder) && (original.Setup == "" || actual.Setup == original.Setup) &&
		(original.Wall == "" || original.Wall == "auto" || actual.Wall == original.Wall)
}

// Prepared fields are validated by their existing SAME-R/SAME-Conn owners.
// The facade also keeps the original command tuple from being replaced by a
// caller before those owners are created or at the final New call.
func (a *HostAttemptV1) checkOriginalIntentV1(ctx context.Context, actual NewArgs) error {
	original := a.plan.args
	if !sameHostAttemptCoreIntentV1(original, actual) {
		return grantOperationError()
	}
	a.request.mu.Lock()
	operation, secrets := a.request.preparationSettings, a.request.newSecrets
	a.request.mu.Unlock()
	inline, memory := original.Inline, original.MemoryMax
	if operation != nil {
		settings, err := operation.Settings(ctx)
		if err != nil {
			return err
		}
		// Derive these two defaults directly. SessionArgs also reads terminal
		// environment values, which belong to protected preparation, not here.
		inline = original.Inline || settings.Agents.Inline
		if original.MemoryMax == 0 {
			memory, err = SettingsMemoryMax(settings)
			if err != nil {
				return err
			}
			if memory == 0 {
				memory = MemoryAuto
			}
		}
	}
	if secrets != nil {
		secrets.mu.Lock()
		valid := secrets.conn == a.conn && !secrets.closed && !secrets.retired && !secrets.aborting
		selectedMemory := secrets.header.MemoryMax
		secrets.mu.Unlock()
		if !valid || original.MemoryMax > 0 && selectedMemory != original.MemoryMax {
			return grantOperationError()
		}
		// The canonical VM builder may replace auto with the complete native
		// unit cap. Its existing arguments check retains the actual Need.
		memory = selectedMemory
	}
	if actual.Inline != inline || actual.MemoryMax != memory {
		return grantOperationError()
	}
	return nil
}
