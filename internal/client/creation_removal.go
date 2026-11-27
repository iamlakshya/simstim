package client

import "context"

// CheckHostCreationRemovalContext completes the reservation's existing-only
// preflight before provider work. It grants no JSON writer or remote identity.
// Existing receipts remain preserved until their canonical owner resolves them.
func CheckHostCreationRemovalContext(ctx context.Context, name string) error {
	return withHostCreationRemovalInspection(ctx, name, func(view stateFileView) error {
		if view.Exists {
			return &UserError{Msg: "an earlier removal receipt still protects this host name",
				Hint: "Keep that receipt and inspect its original host and unfinished cleanup before reusing the name.", Code: "removal_record", Kind: KindRefused}
		}
		return ctx.Err()
	})
}
