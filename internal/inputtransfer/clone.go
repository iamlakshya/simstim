package inputtransfer

import (
	"context"
	"os"

	"github.com/iamlakshya/simstim/internal/api"
)

// CloneOperations is the Init owner's fixed native Git adapter. It accepts
// only a protected repository ordinal and descriptor, never a guest command,
// UID, source pathname, new host seed identity or arbitrary environment.
type CloneOperations struct {
	CloneRepository func(context.Context, int, api.InputRepository, api.GitIdentity) error
}

// CloneStaged rechecks each bundle through the actual owner's pinned stage.
// The owner calls it ONLY after joining its successful fixed receiver helper.
// Every callback uses the SAME owned command process and current phase ctx.
// This function neither emits completion nor starts an agent or publishes state.
func CloneStaged(ctx context.Context, stage *os.Root, control Control, summary api.InputSummary, operations CloneOperations) error {
	if stage == nil || len(summary.Repositories) > 0 && operations.CloneRepository == nil {
		return protocol("the canonical native seed owner is unavailable")
	}
	if err := summary.Validate(control.CopyMax, control.SeedMax); err != nil {
		return err
	}
	if summary.Nonce != control.Nonce || summary.Digest != control.Digest {
		return protocol("the native seed scope changed")
	}
	for index, repository := range summary.Repositories {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := VerifyBundle(ctx, stage, index, repository); err != nil {
			return err
		}
		if err := operations.CloneRepository(ctx, index, repository, summary.Identity); err != nil {
			return err
		}
	}
	return ctx.Err()
}
