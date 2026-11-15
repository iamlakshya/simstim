package client

import "context"

// Schema1 is the canonical pre-wall classifier. Schema2+ None rows cannot
// distinguish a migrated legacy creation from a modern missing receipt.
// This read yields an empty PUBLIC set only. It authorises no literal read.
func (c *Conn) checkOriginalLegacyNonePublicSet(ctx context.Context, id string, original *SandboxPolicy) error {
	if original == nil || original.CreationWall != "none" || original.HostID != c.HostID() ||
		original.FutureLineage || original.FutureDelivery || original.futureDeliveryReceipt || original.futureInputOperation ||
		original.lineage != nil || len(original.lineageRaw) != 0 || original.DeliveryIntent != nil || len(original.deliveryRaw) != 0 ||
		original.deliveryReceipt != nil || len(original.deliveryReceiptRaw) != 0 || original.inputOperation != nil || len(original.inputOperationRaw) != 0 ||
		len(original.wallIntent) != 0 || len(original.PolicySources) != 0 || len(original.Exposed) != 0 {
		return reviewAuthorityError()
	}
	return withSandboxReceiptObservation(ctx, c, func(owned context.Context) error {
		return withSandboxFile(owned, c.Host, false, func(file *sandboxFile) error {
			if file.schema != 1 {
				return reviewAuthorityError()
			}
			for name := range file.root {
				if name != "schema" && name != "host" && name != "sessions" {
					return reviewAuthorityError()
				}
			}
			raw, present := file.sessions[id]
			if !present {
				return reviewAuthorityError()
			}
			// Reuse the canonical duplicate/alias scanner and original decoder.
			// No unknown/modern field can be erased by the typed view's zeroes.
			fields, err := sandboxObjectContext(owned, raw, "session_id", "host_id", "ceiling", "ssh_agent", "secrets", "created_at", "request")
			if err != nil {
				return err
			}
			for name := range fields {
				switch name {
				case "session_id", "host_id", "ceiling", "ssh_agent", "secrets", "created_at", "request":
				default:
					return reviewAuthorityError()
				}
			}
			current, err := decodeSandboxContext(owned, id, raw, file.schema)
			if err != nil {
				return err
			}
			if !sameSandboxCreation(original, &current) || current.CreationWall != "none" || current.HostID != c.HostID() {
				return reviewAuthorityError()
			}
			return c.BindHostStateObservation(owned)
		})
	})
}
