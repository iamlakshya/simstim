package client

import (
	"context"

	"github.com/iamlakshya/simstim/internal/inputtransfer"
)

func pendingInputResumeError() error {
	return &UserError{Msg: "the original input operation must finish before selecting new resume values",
		Hint: "Finish the original input operation, or prepare its authorised copied inputs before resuming.", Code: "input_pending", Kind: KindRefused}
}

// This is an early restriction, before any new value selection or command.
// A list row can refuse a resume; it cannot clear a local receipt or mint a
// payload/value capability. The actual host keeps its strict input/run CAS.
func (c *Conn) checkNoPendingInputResume(ctx context.Context, id string) error {
	pending, err := HasPendingSessionInputReceipt(ctx, c.Host, id)
	if err != nil {
		return err
	}
	if pending {
		return pendingInputResumeError()
	}
	if !c.HasFeature(inputtransfer.Feature) {
		return nil
	}
	if err := bindRequestTransportSource(ctx, c); err != nil {
		return err
	}
	rows, err := c.Sessions(ctx)
	if err != nil {
		return err
	}
	if err := bindRequestTransportSource(ctx, c); err != nil {
		return err
	}
	var selected *Session
	for index := range rows {
		if rows[index].ID == id {
			selected = &rows[index]
			break
		}
	}
	if selected == nil {
		return sandboxReceiptError()
	}
	if selected.CopiesPending {
		return pendingInputResumeError()
	}
	if selected.SandboxOf != "" {
		pending, err := HasPendingSessionInputReceipt(ctx, c.Host, selected.SandboxOf)
		if err != nil {
			return err
		}
		if pending {
			return pendingInputResumeError()
		}
		for _, root := range rows {
			if root.ID == selected.SandboxOf {
				if root.CopiesPending {
					return pendingInputResumeError()
				}
				return nil
			}
		}
		return sandboxReceiptError()
	}
	return nil
}
