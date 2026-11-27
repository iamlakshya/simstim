package client

import "context"

// Only the actual pre-provider creation reservation may use this fixed-file
// seam. H15 supplies the one pure completed destroy-only owner predicate.
// Nothing here proves a provider resource absent or grants checked identity.
func clearHostCreationRemovalRecordContext(ctx context.Context, name string, validate func(stateFileView) error) (removed bool, failure error) {
	failure = withHostCreationRemovalState(ctx, name, func(tx *stateFileTxn) error {
		if validate == nil {
			return hostStateRemovalError("creation journal clearing has no canonical owner predicate")
		}
		if tx.source == nil {
			return nil
		}
		if err := tx.removalOwnership(); err != nil {
			return err
		}
		if err := validate(tx.view()); err != nil {
			return err
		}
		if err := tx.removalOwnership(); err != nil {
			return err
		}
		tx.revokeReader()
		err := tx.removeExact()
		removed = tx.removed
		return err
	})
	return removed, failure
}
