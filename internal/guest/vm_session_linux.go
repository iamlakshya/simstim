//go:build linux

package guest

import (
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
)

func (v *vmRuntime) validateSession(a proto.CtlStartArgs) error {
	if !v.prepared() || a.Session != v.plan.Args.Session || a.CaptureNonce != v.plan.Args.CaptureNonce || !equalVMStrings(a.StateFiles, v.plan.Args.StateFiles) {
		return vmError("session infrastructure conflicts with the protected prepared VM plan")
	}
	return v.setupDNS(a.DNS)
}
func (v *vmRuntime) setupDNS(mode string) error {
	if !v.prepared() || v.network == nil || v.network.dns == nil || mode != proto.DNSSynthetic {
		return vmError("the prepared VM requires its actual synthetic DNS transport")
	}
	return nil
}
func (v *vmRuntime) memberProxyEnv(a *proto.CtlStartArgs, _ bool) error {
	if !v.prepared() || v.network == nil || v.network.proxy == nil || a.Proxy != "" && a.Proxy != host.DefaultGuestProxy {
		return vmError("the VM member proxy conflicts with its prepared private service")
	}
	// Keep the fixed loopback URI even when the SAME service also serves the
	// private Docker bridge. Its wildcard bind address is not an agent target.
	a.Env = rewriteProxyEnv(a.Env, host.DefaultGuestProxy)
	return nil
}
