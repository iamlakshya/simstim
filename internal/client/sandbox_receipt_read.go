package client

import "context"

// This read yields cloned historical metadata only. It neither constructs a
// review/Finish capability nor reads a value, starts a helper or sends an RPC.
func getSandboxReceiptPolicyContext(ctx context.Context, c *Conn, id string) (*SandboxPolicy, error) {
	var found *SandboxPolicy
	err := withSandboxReceiptObservation(ctx, c, func(owned context.Context) error {
		row, err := LoadSandboxPolicy(owned, c.Host, id)
		if err != nil || row == nil {
			return err
		}
		if row.HostID != c.HostID() {
			return sandboxReceiptError()
		}
		for _, scope := range sandboxReceiptPolicyScopes(row) {
			if scope.Host != c.Host.Name || !scope.matchesPolicy(row) {
				return sandboxReceiptError()
			}
			if err := scope.Source.check(owned, c); err != nil {
				return err
			}
		}
		found = cloneSandboxPolicy(row)
		return nil
	})
	return found, err
}

func sandboxReceiptPolicyScopes(row *SandboxPolicy) []sandboxReceiptScope {
	var scopes []sandboxReceiptScope
	if row.deliveryReceipt != nil {
		scopes = append(scopes, row.deliveryReceipt.Scope)
	}
	if row.inputOperation != nil {
		scopes = append(scopes, row.inputOperation.Scope)
	}
	return scopes
}

// The actual transport stamp supplies modern checked identity and ORIGINAL
// Source. A newly opened local OP cannot relabel an old Conn or saved receipt.
func withSandboxReceiptObservation(ctx context.Context, c *Conn, run func(context.Context) error) error {
	if c == nil || c.hostTransport == nil || !ValidHostID(c.HostID()) || c.hostTransport.kind != hostTransportSaved {
		return sandboxReceiptError()
	}
	if err := c.hostTransport.checkConn(c); err != nil {
		return err
	}
	owned, unlock, err := LockHost(ctx, c.Host.Name, "local operation receipt")
	if err != nil {
		return err
	}
	defer unlock()
	o, _ := owned.Value(hostStateObservationKey{}).(*HostStateObservation)
	if o == nil {
		o, err = ObserveHostStateContext(owned, c.Host, c.HostID())
		if err != nil {
			return err
		}
		owned = WithHostStateObservation(owned, o)
	}
	if err := c.BindHostStateObservation(owned); err != nil {
		return err
	}
	return run(owned)
}
