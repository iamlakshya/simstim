package tui

import (
	"fmt"
	"net"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

// pendingNetworkSummary is display only. In particular its formatted host
// cannot be parsed into a policy target or used in place of the opaque ID.
func pendingNetworkSummary(request api.PendingNetworkRequest, now time.Time) string {
	if !api.ValidPendingNetworkRequest(request) {
		return "network request unavailable"
	}
	target := net.JoinHostPort(shown(request.Host), fmt.Sprint(request.Port))
	if request.Redacted {
		target += " (private)"
	}
	if request.Reason == api.NetworkHoldUploadBudget {
		target += " (upload budget; once or refuse)"
	}
	if request.Status == api.NetworkHoldClosed {
		return target + dot + "closed"
	}
	waiting := "waiting for a retry"
	if request.Waiting == 1 {
		waiting = "1 flow waiting"
	} else if request.Waiting > 1 {
		waiting = fmt.Sprintf("%d flows waiting", request.Waiting)
	}
	remaining := "expired"
	if request.Expires.After(now) {
		remaining = "open until " + request.Expires.UTC().Format("15:04:05 UTC")
	}
	return target + dot + waiting + dot + remaining
}

// pendingNetworkSummaries draws every record, without using the masked host
// as an identity. Scrolling may limit the viewport, never the retained list.
func pendingNetworkSummaries(snapshot api.NetworkHoldSnapshot, now time.Time) []string {
	if !api.ValidNetworkHoldSnapshot(snapshot) || snapshot.Stopped {
		return []string{"network requests unavailable"}
	}
	if len(snapshot.Requests) == 0 {
		return []string{"no pending network requests"}
	}
	lines := make([]string, 0, len(snapshot.Requests))
	for _, request := range snapshot.Requests {
		lines = append(lines, pendingNetworkSummary(request, now))
	}
	return lines
}
