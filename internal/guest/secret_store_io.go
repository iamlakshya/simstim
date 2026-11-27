package guest

import (
	"context"
	"errors"
	"sync"
)

const maxSecretStoreOutput = 64 << 10

var errSecretStoreOutput = errors.New("secret Store output exceeds its limit")

// Stdout and stderr share one batch budget. No byte is retained. Cancellation
// uses the owned command's existing group cleanup and waiter.
type secretStoreDiscard struct {
	mu        sync.Mutex
	remaining int
	overflow  bool
	cancel    context.CancelFunc
}

func (output *secretStoreDiscard) Write(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	output.mu.Lock()
	if output.overflow || len(data) > output.remaining {
		output.overflow, output.remaining = true, 0
		output.mu.Unlock()
		output.cancel()
		return 0, errSecretStoreOutput
	}
	output.remaining -= len(data)
	output.mu.Unlock()
	return len(data), nil
}

func (output *secretStoreDiscard) exceeded() bool {
	output.mu.Lock()
	defer output.mu.Unlock()
	return output.overflow
}
