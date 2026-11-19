package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/tomlinspect"
)

type agentLedgerModeKey struct{}

// The actual agent supplies its existing mode. Other explicit ledger callers
// own a directory-only mode on their same request; no parsed Settings or second
// slab is created for a live agent and no resource grants outbound authority.
func recordAgentGrantFD(ctx context.Context, h Host, hostID string) (returned error) {
	standalone := grantRequestOf(ctx) == nil
	ctx, request := ensureGrantRequest(ctx)
	if standalone {
		defer func() { returned = errors.Join(returned, request.Close()) }()
	}
	return withStateTransaction(ctx, nil, grantsFileName, true, false, func(tx *stateFileTxn) (failure error) {
		if !tx.view().Exists {
			now := nowUTC()
			fresh := grantsFile{Version: 1, Grants: []GrantRecord{{HostName: h.Name, HostID: hostID, Kind: OutboundSSHAgent, FirstAt: now, LastAt: now, Count: 1}}}
			data, err := encodeGrantsFile(fresh)
			if err != nil {
				return err
			}
			return writeAgentLedgerReplacement(tx, func(_ context.Context, w io.Writer) error { return writeLedgerBytes(w, data) })
		}
		mode, _ := ctx.Value(agentLedgerModeKey{}).(*agentModeResources)
		if mode == nil {
			var err error
			mode, err = request.borrowMetadataMode(tx.ctx)
			if err != nil {
				return err
			}
		}
		snapshot, err := openAgentSettingsSnapshot(tx.ctx)
		if err != nil {
			return agentSettingsFailure(err)
		}
		defer func() { failure = errors.Join(failure, snapshot.close()) }()
		// The index and original FD view remain live only inside the existing
		// private-part callback. Source checks/index close precede its return,
		// so the SAME transaction can then publish and flush its replacement.
		return writeAgentLedgerReplacement(tx, func(_ context.Context, out io.Writer) error {
			scope, err := newAgentLedgerMetadataScope(mode, snapshot, tx)
			if err != nil {
				return err
			}
			defer scope.close()
			return mode.inspector.engine.WithSources(scope.ctx, scope.descriptors(), func(inspection *tomlinspect.Inspection) error {
				defer scope.finishCachedWork()
				if err := inspection.InspectTOML(0, &mode.inspector.visitor); err != nil {
					return agentSettingsFailure(err)
				}
				return writeBoundedAgentLedger(inspection, out, h, hostID)
			})
		})
	})
}

func writeAgentLedgerReplacement(tx *stateFileTxn, replace stateFileReplace) error {
	err := tx.replace(replace)
	var callback *stateCallbackError
	if errors.As(err, &callback) && err == callback {
		return agentLedgerProjectionError(tx.ctx, callback.err)
	}
	return agentLedgerProjectionError(tx.ctx, err)
}

func writeBoundedAgentLedger(inspection *tomlinspect.Inspection, out io.Writer, h Host, hostID string) error {
	ctx, err := inspection.Context()
	if err != nil {
		return err
	}
	source, err := inspection.Source(1)
	if err != nil {
		return err
	}
	projection, err := api.NewJSONProjection(ctx, source, source.Size())
	if err != nil {
		return err
	}
	guarded, err := newAgentJSONProjection(projection, inspection)
	if err != nil {
		return err
	}
	completed, err := newLedgerEmissionSource(inspection, guarded, h, hostID)
	if err != nil {
		return err
	}
	ledger := completed.ledger
	now := nowUTC()
	emission := &ledgerEmission{ledger: ledger, completed: completed, inspection: inspection, h: h, hostID: hostID, now: now}
	if !ledger.found {
		emission.newJSON, err = json.Marshal(GrantRecord{HostName: h.Name, HostID: hostID, Kind: OutboundSSHAgent, FirstAt: now, LastAt: now, Count: 1})
		if err != nil {
			return err
		}
	}
	return emission.write(out)
}
