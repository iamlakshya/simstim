//go:build !linux

package jail

import "context"

func (c *Cmd) CheckNativeLiveV1(ctx context.Context) error {
	return failure("native jail liveness requires the original Linux creation owner", "Keep the original native runtime and use a supported host.")
}
