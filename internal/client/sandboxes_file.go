package client

import "context"

func badSandboxState() error { return badGrantStateError("sandboxes.json") }

func badSandboxStateContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return badSandboxState()
}

// The ledger and session policy use the same pinned, locked state writer.
// A security fix to that writer must apply to both records together.
func withSandboxFile(ctx context.Context, h Host, write bool, change func(*sandboxFile) error) error {
	if !write {
		return withHostStateReadContext(ctx, h, func(owned context.Context) error { return withSandboxFileObserved(owned, h, false, change) })
	}
	return withSandboxFileObserved(ctx, h, true, change)
}

func withSandboxFileObserved(ctx context.Context, h Host, write bool, change func(*sandboxFile) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !ValidHostName(h.Name) {
		return badHostNameError(h.Name)
	}
	return withStateFile(ctx, []string{"hosts", HostFileKey(h.Name)}, "sandboxes.json", write,
		func(raw []byte, exists bool) ([]byte, error) {
			return changeSandboxFileContext(ctx, h, raw, exists, write, change)
		})
}

// Both ordinary byte callers and purpose-owned existing-only mutations share
// this ONE canonical decode/change/raw-preserving encode adapter.
func changeSandboxFileContext(ctx context.Context, h Host, raw []byte, exists, write bool, change func(*sandboxFile) error) ([]byte, error) {
	f := emptySandboxFile(h)
	if exists {
		var err error
		f, err = decodeSandboxFileContext(ctx, raw, h)
		if err != nil {
			return nil, err
		}
	}
	if write {
		if err := upgradeSandboxIntentContext(ctx, f); err != nil {
			return nil, err
		}
	}
	if err := change(f); err != nil {
		return nil, err
	}
	if !write {
		return nil, nil
	}
	var err error
	f.root["sessions"], err = marshalSandboxObject(f.sessions)
	if err != nil {
		return nil, err
	}
	b, err := marshalSandboxObject(f.root)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
