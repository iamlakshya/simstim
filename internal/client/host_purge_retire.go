package client

import (
	"context"
	"io"
)

// FinishPurgedHostRetirement opens only an existing post-forget operation.
// It neither creates a source nor reaches, wakes or installs anything remotely.
func FinishPurgedHostRetirement(ctx context.Context, name string) (bool, error) {
	var record purgeRemovalReceiptV1
	inspect := func(inspecting context.Context, reader io.ReaderAt, size int64, expected string) (HostStateReceiptIdentity, error) {
		identity, err := inspectPurgeRemovalV1(inspecting, reader, size, expected)
		if err != nil {
			return identity, err
		}
		raw, err := io.ReadAll(io.NewSectionReader(reader, 0, size))
		if err != nil {
			return identity, err
		}
		record, err = decodePurgeRemovalV1(raw, identity.Original)
		return identity, err
	}
	return WithHostStateRetirementContext(ctx, name, inspect, func(verified context.Context, identity HostStateReceiptIdentity) error {
		if record.Nonce != identity.Nonce || record.HostID != identity.HostID {
			return hostStateRemovalError("the terminal purge receipt changed during native inspection")
		}
		if err := MarkGrantsRemovedContext(verified, identity.Original); err != nil {
			return err
		}
		_, err := RemoveHostStateContext(verified, identity.Original, identity.HostID, purgeLocalValidatorsV1(verified, record))
		return err
	})
}
