package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/iamlakshya/simstim/internal/client"
)

// forgetRemovedHost commits the saved-entry forget and reports a cancel
// with the completed deletion, if any. Cleanup warnings follow success.
func forgetRemovedHost(ctx context.Context, p *prompter, h client.Host, deleted bool) error {
	if err := client.UpdateHosts(ctx, client.HostChange{Kind: "host rm", Was: &h}); err != nil {
		err = forgetFailed(h, err, deleted)
		if errors.Is(err, context.Canceled) && len(p.changes().Lines()) > 0 {
			return p.cancelOf(err)
		}
		return err
	}
	p.changes().Add("forgot the host %s", h.Name)
	if err := client.ClearRemoval(h.Name); err != nil {
		// The host is forgotten: the record that stays is a warning.
		fmt.Fprintf(p.out, "%s.\n", sentenceCase(shown(err.Error())))
		if hint := hintOf(err); hint != "" {
			fmt.Fprintf(p.out, "Try: %s\n", shown(hint))
		}
	}
	return nil
}

// forgetFailed is the error of a forget that failed. The host is still
// saved, so the command can run again, and says so. When the machine is
// deleted already, the error says that too. A cancel, as at Ctrl+C, stays
// a cancel. A user error keeps its own code and hint, such as host_changed
// for a host that another command changed, or hosts_busy.
func forgetFailed(h client.Host, err error, deleted bool) error {
	if errors.Is(err, context.Canceled) {
		return err
	}
	var ue *client.UserError
	if errors.As(err, &ue) {
		if !deleted {
			return err
		}
		kept := *ue
		kept.Msg = fmt.Sprintf("the machine of %s is deleted, but %s is still saved: %s", h.Name, h.Name, ue.Msg)
		return &kept
	}
	msg := fmt.Sprintf("could not forget %s: %s", h.Name, err.Error())
	if deleted {
		msg = fmt.Sprintf("the machine of %s is deleted, but %s is still saved: %s", h.Name, h.Name, err.Error())
	}
	return &client.UserError{Msg: msg, Hint: rmOpts{name: h.Name}.line() + " (after you fix the cause)", Code: "forget_failed"}
}
