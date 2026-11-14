package client

import (
	"context"
	"errors"
)

// Doctor asks the host for its current checks and sandbox capabilities.
// An empty release URL keeps the probe local to the selected host.
func (c *Conn) Doctor(ctx context.Context) (HostInfo, error) {
	var info HostInfo
	if err := c.Call(ctx, "doctor", nil, &info); err != nil {
		return info, err
	}
	if !c.HasFeature("sandbox") {
		return info, nil
	}
	probe, err := c.CapProbe(ctx, "")
	if stopped := ctx.Err(); stopped != nil {
		return info, stopped
	}
	if err != nil {
		hint := "Check the sandbox runtime installation, then try again."
		var user *UserError
		if errors.As(err, &user) && user.Hint != "" {
			hint = user.Hint
		}
		info.Checks = append(info.Checks, Check{Name: "sandbox capabilities", Detail: CleanLine(err.Error()), Fix: hint})
		return info, nil
	}
	info.Sandbox = &probe
	return info, nil
}
