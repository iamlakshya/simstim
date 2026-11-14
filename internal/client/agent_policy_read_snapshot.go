package client

import (
	"context"
	"io"
)

// Agent facts share the same lower acquisition and final inode snapshots as
// canonical readers and writers, without materialising opaque metadata.
func withAgentPolicySnapshot(ctx context.Context, h Host, read func(io.ReaderAt, int64, bool) error) error {
	return withHostStateReadContext(ctx, h, func(owned context.Context) error {
		return withStateTransaction(owned, []string{"hosts", HostFileKey(h.Name)}, "sandboxes.json", false, false, func(tx *stateFileTxn) error {
			view := tx.view()
			err := read(view.ReaderAt, view.Size, view.Exists)
			if !view.Exists {
				return err
			}
			return wrapGrantStateError("sandboxes.json", err)
		})
	})
}
