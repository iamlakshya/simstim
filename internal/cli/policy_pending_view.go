package cli

import (
	"fmt"
	"io"
	"net"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

// writePendingNetworkRequest draws one validated public descriptor. Host is
// display text even when it is not redacted; actions must retain the raw ID.
func writePendingNetworkRequest(out io.Writer, request api.PendingNetworkRequest, now time.Time) error {
	if !api.ValidPendingNetworkRequest(request) {
		return &client.UserError{Code: "policy_pending_invalid", Kind: client.KindFailed,
			Msg: "the host returned an invalid pending network request", Hint: "Upgrade the helper and read the pending requests again."}
	}
	target := net.JoinHostPort(client.CleanLine(request.Host), fmt.Sprint(request.Port))
	if request.Redacted {
		target += " (private)"
	}
	if request.Reason == api.NetworkHoldUploadBudget {
		target += " (upload budget; once or refuse)"
	}
	state := "expired"
	if request.Status == api.NetworkHoldClosed {
		state = "closed"
	} else if request.Expires.After(now) {
		state = "until " + request.Expires.UTC().Format(time.RFC3339)
	}
	_, err := fmt.Fprintf(out, "%s\t%s\t%d waiting\t%s\n", request.ID, target, request.Waiting, state)
	if err != nil {
		return &client.UserError{Code: "output", Kind: client.KindFailed,
			Msg: "could not write pending network requests", Hint: "Check the output destination and read the requests again."}
	}
	return nil
}

// writePendingNetworkSnapshot validates the complete list before writing any
// row. The caller handles empty/stopped messages on its diagnostic stream.
func writePendingNetworkSnapshot(out io.Writer, snapshot api.NetworkHoldSnapshot, now time.Time) error {
	if !api.ValidNetworkHoldSnapshot(snapshot) {
		return &client.UserError{Code: "policy_pending_invalid", Kind: client.KindFailed,
			Msg: "the host returned an invalid pending network snapshot", Hint: "Upgrade the helper and read the pending requests again."}
	}
	if snapshot.Stopped {
		return nil
	}
	for _, request := range snapshot.Requests {
		if err := writePendingNetworkRequest(out, request, now); err != nil {
			return err
		}
	}
	return nil
}
