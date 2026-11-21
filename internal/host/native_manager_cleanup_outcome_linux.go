//go:build linux

package host

import (
	"context"
	"log/slog"
)

// Resource progress follows only the actual same-object native result.
// Original diagnostics remain on the owning producer and its report boundary.
func closeNativeManagerResourceV4(manager *nativeUserManagerV1, primary string, diagnostic *error) error {
	outcome, unknown := retireNativeManagerV4(context.Background(), manager)
	if outcome != nil && *diagnostic == nil {
		*diagnostic = outcome
		if validID(primary) {
			warnSession(primary, "original native manager cleanup reported an I/O outcome", "err", outcome)
		} else {
			slog.Warn("original native manager cleanup reported an I/O outcome", "err", outcome)
		}
	}
	return unknown
}
