package client

import "context"

// Accepted original-machine bridges stay retained until their actual single
// waiter has joined. Conn.Close's logical Done alone is not that observation.
func (o *SessionMachineOwnerV1) retainConnectionV1(c *Conn) {
	if o == nil || c == nil || c.route.machineOwner != o {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.connections == nil {
		o.connections = make(map[*Conn]struct{})
	}
	o.connections[c] = struct{}{}
}

func (o *SessionMachineOwnerV1) joinConnectionsV1(ctx context.Context) error {
	o.mu.Lock()
	connections := make([]*Conn, 0, len(o.connections))
	for conn := range o.connections {
		connections = append(connections, conn)
	}
	o.mu.Unlock()
	for _, conn := range connections {
		if err := joinSessionMachineConnV1(ctx, conn); err != nil {
			return err
		}
		o.mu.Lock()
		delete(o.connections, conn)
		o.mu.Unlock()
	}
	return nil
}

func joinSessionMachineConnV1(ctx context.Context, conn *Conn) error {
	conn.Close()
	if conn.procDone == nil {
		return sessionMachineErrorV1("the machine bridge has no original process waiter")
	}
	select {
	case <-conn.procDone:
	case <-ctx.Done():
		return &UserError{Msg: "the machine's original bridge has not finished closing", Hint: "Keep its saved host and wait for the original connection to close before retrying removal.", Code: "machine_active", Kind: KindFailed}
	}
	if conn.machineLeaseDone != nil {
		select {
		case <-conn.machineLeaseDone:
		case <-ctx.Done():
			return &UserError{Msg: "the machine's original lease renewal has not finished", Hint: "Keep its saved host and wait for the original renewal to finish before retrying removal.", Code: "machine_active", Kind: KindFailed}
		}
	}
	return nil
}
