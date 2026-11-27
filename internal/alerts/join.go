package alerts

import (
	"context"
	"errors"
)

// Join waits for the actual dispatcher loop and delivery workers after Close.
// A cancelled wait leaves those real owners unconfirmed. It never treats the
// bounded Close grace as proof that a command or network delivery has ended.
func (d *Dispatcher) Join(ctx context.Context) error {
	if d == nil {
		return nil
	}
	if ctx == nil || d.done == nil || d.loopDone == nil {
		return errors.New("the alert dispatcher has no original worker lifetime")
	}
	for _, done := range []<-chan struct{}{d.loopDone, d.done} {
		select {
		case <-done:
			continue
		default:
		}
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
