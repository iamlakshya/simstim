package client

import (
	"context"
	"encoding/json"

	"github.com/iamlakshya/simstim/internal/api"
)

func purgeLocalValidatorsV1(ctx context.Context, record purgeRemovalReceiptV1) map[string]func([]byte) error {
	return map[string]func([]byte) error{
		"baseline.json": func(raw []byte) error {
			_, err := decodeBaselineV1(raw, record.Original.Name, record.HostID)
			return err
		},
		"root-steps.json": func(raw []byte) error {
			fields, err := api.JSONObjectFields(raw, "version", "host", "records", "started")
			if err != nil {
				return badGrantStateError("root-steps.json")
			}
			var version int
			var host string
			var records []json.RawMessage
			var started map[string]bool
			if json.Unmarshal(fields["version"], &version) != nil || version != 1 || json.Unmarshal(fields["host"], &host) != nil || host != record.Original.Name {
				return badGrantStateError("root-steps.json")
			}
			if data, ok := fields["records"]; ok {
				if json.Unmarshal(data, &records) != nil {
					return badGrantStateError("root-steps.json")
				}
			}
			if data, ok := fields["started"]; ok {
				if json.Unmarshal(data, &started) != nil {
					return badGrantStateError("root-steps.json")
				}
			}
			for id := range started {
				if !ValidHostID(id) {
					return badGrantStateError("root-steps.json")
				}
			}
			for _, entry := range records {
				var step RootStepRecord
				if !api.ExactJSONFields(entry, step) || json.Unmarshal(entry, &step) != nil || !validRootRecord(step) {
					return badGrantStateError("root-steps.json")
				}
			}
			return ctx.Err()
		},
		"sandboxes.json": func(raw []byte) error {
			file, err := decodeSandboxFileContext(ctx, raw, record.Original)
			if err != nil {
				return err
			}
			if _, err := api.JSONObjectFields(raw, "schema", "host", "sessions"); err != nil {
				return badSandboxState()
			}
			for id, row := range file.sessions {
				policy, err := decodeSandboxContext(ctx, id, row, file.schema)
				if err != nil {
					return err
				}
				if policy.FutureLineage || policy.FutureDelivery || policy.futureDeliveryReceipt || policy.futureInputOperation {
					return badSandboxState()
				}
			}
			return ctx.Err()
		},
		"removal.json": func(raw []byte) error {
			current, err := decodePurgeRemovalV1(raw, record.Original)
			if err != nil {
				return err
			}
			if !current.complete() || current.Nonce != record.Nonce || current.HostID != record.HostID || current.TargetGeneration == "" || !sameReceiptSource(current.Source, record.Source) {
				return hostStateRemovalError("the final local removal receipt changed its original owner or attempt")
			}
			return ctx.Err()
		},
	}
}
