package client

import (
	"context"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/supply"
)

// capProbeForSelectedRuntime has no raw-manifest entry point. Its private
// selection is issued only after actual delivery and runtimeReady accepted
// the original checked release request on this same Conn/Source/HID.
// The host validates its own selected receipt before its read-only probe;
// this request carries no SessionOP, Need or permission to start a session.
func (c *Conn) capProbeForSelectedRuntime(ctx context.Context, selected *newRuntimeSelection) (api.CapProbe, error) {
	var result api.CapProbe
	if err := requireGrantOperation(ctx, grantOpNew); err != nil {
		return result, err
	}
	manifest, err := selected.forArgs(ctx, c, nil)
	if err != nil {
		return result, err
	}
	if err := c.Call(ctx, "sandbox.probe", struct {
		Runtime *supply.Manifest `json:"runtime"`
	}{Runtime: manifest}, &result); err != nil {
		return result, err
	}
	if _, err := selected.forArgs(ctx, c, manifest); err != nil {
		return api.CapProbe{}, err
	}
	return result, nil
}
