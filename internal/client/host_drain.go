package client

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
)

// Drain changes the user drain setting on this original accepted saved-host
// connection. It grants no runtime, session, native unit or provider action.
func (c *Conn) Drain(ctx context.Context, on bool, reason string) (api.DrainState, error) {
	var result api.DrainState
	if on && (reason == "" || reason == "off" || len(reason) > 512 || strings.TrimSpace(reason) != reason || api.CleanText(reason, 512) != reason) || !on && reason != "" {
		return result, &UserError{Msg: "the drain reason must be one plain line of 1 to 512 bytes", Hint: "Use drain=off to clear it, or give a short reason.", Code: "bad_drain", Kind: KindUsage}
	}
	if err := checkFactsConnection(ctx, c); err != nil {
		return result, err
	}
	stamp := c.hostTransport
	if !c.HasFeature("drain") {
		return result, c.grantHelperOld("change the host drain setting")
	}
	var raw json.RawMessage
	if err := c.Call(ctx, "host.drain", api.DrainArgs{On: on, Reason: reason}, &raw); err != nil {
		return result, err
	}
	fields, shapeErr := api.JSONObjectFields(raw, api.ExactJSONFieldNames(result)...)
	state := bytes.TrimSpace(fields["on"])
	if shapeErr != nil || len(fields) != 2 || !bytes.Equal(state, []byte("true")) && !bytes.Equal(state, []byte("false")) || !bytes.HasPrefix(bytes.TrimSpace(fields["reason"]), []byte(`"`)) || !api.ExactJSONFields(raw, result) || json.Unmarshal(raw, &result) != nil || result.On != on || result.Reason != reason {
		return api.DrainState{}, &UserError{Msg: "the host returned an inconsistent drain setting", Hint: "Check host doctor through the current connection before retrying.", Code: "drain_protocol", Kind: KindRefused}
	}
	if c.hostTransport != stamp {
		return api.DrainState{}, grantIdentityError(c.Host)
	}
	if err := checkFactsConnection(ctx, c); err != nil {
		return api.DrainState{}, err
	}
	return result, nil
}
