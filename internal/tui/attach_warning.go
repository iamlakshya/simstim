package tui

import (
	"context"
	"fmt"
	"os"

	"github.com/iamlakshya/simstim/internal/client"
)

// The launcher has restored and released its terminal before this context is
// used. Reconnect masks the warning sink again before any raw-reader work.
func terminalAttachContextV1(ctx context.Context) context.Context {
	return client.WithWarnings(ctx, func(_ string, message, hint string) {
		fmt.Fprintf(os.Stderr, "Warning: %s\n", client.CleanLine(message))
		if hint != "" {
			fmt.Fprintf(os.Stderr, "Try: %s\n", client.CleanLine(hint))
		}
	})
}
