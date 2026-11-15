package client

import (
	"context"
	"path"
	"strconv"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
)

func portsUpgrade() error {
	return &UserError{Msg: "the helper cannot forward sandbox ports", Hint: "Upgrade the host helper, then stop and resume the sandbox.", Code: "upgrade_required", Kind: KindRefused}
}

// GrantPort grants one guest loopback port and returns its exact private
// supervisor socket. A claimed guest address never selects an SSH destination.
func (c *Conn) GrantPort(ctx context.Context, session string, port int) (string, error) {
	if err := checkSessionID(c.Host.Name, session); err != nil {
		return "", err
	}
	if port < 1 || port > 65535 {
		return "", &UserError{Msg: "port must be from 1 to 65535", Hint: "Name one decimal TCP port.", Code: "bad_port", Kind: KindUsage}
	}
	if !c.HasFeature(api.FeatureSandboxPortsV1) {
		return "", portsUpgrade()
	}
	ctx,_=ensureGrantRequest(ctx)
	ctx, release, err := c.beginGrant(ctx, "port forward", false)
	if err != nil {
		return "", err
	}
	defer release()
	tier, creation, err := EffectiveSessionTier(ctx, c.Host, session, c.HostID())
	if err != nil {
		return "", err
	}
	if creation == nil || creation.FutureLineage || creation.CreationWall == "" || creation.CreationWall == "none" {
		return "", &UserError{Msg: "the sandbox has no supported local creation record", Hint: "Resume it through the current client before forwarding its ports.", Code: "grant_state", Kind: KindRefused}
	}
	settings, err := ReadPolicySettings()
	if err != nil {
		return "", err
	}
	if err := Allow(c.Host, tier, Outbound{Kind: OutboundForward}, settings); err != nil {
		return "", err
	}
	primary := session
	if creation.lineage != nil {
		primary = creation.lineage.Root
	}
	var result api.PortGrantResultV1
	if err := c.callWithSent(ctx, "port.grant", api.PortArgsV1{Version: 1, ID: session, Port: port}, &result, release); err != nil {
		return "", err
	}
	if result.Version != 1 || result.Port != port || !validPortSocket(result.Socket, primary, port) {
		return "", &UserError{Msg: "the supervisor returned an invalid port socket", Hint: "Upgrade the helper and check the sandbox before forwarding again.", Code: "protocol", Kind: KindRefused}
	}
	return result.Socket, nil
}

func validPortSocket(socket, primary string, port int) bool {
	return api.ValidSessionID(primary) && path.IsAbs(socket) && path.Clean(socket) == socket && len(socket) <= 103 && CleanLine(socket) == socket && !strings.ContainsAny(socket, ":\x00\r\n%[]") && strings.HasSuffix(socket, "/p/"+primary+"/"+strconv.Itoa(port)+".sock")
}
