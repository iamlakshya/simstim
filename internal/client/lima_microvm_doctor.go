package client

import (
	"github.com/iamlakshya/simstim/internal/api"
	"runtime"
)

func (c *Conn) limaMicroVMChecksV1(probe api.CapProbe) []Check {
	return limaMicroVMGuestChecksV1(c.Host, probe, runtime.GOOS, runtime.GOARCH)
}

// Only an actual guest's missing KVM device triggers this diagnosis. A Mac
// model, requested nested flag or saved label never proves availability.
func limaMicroVMGuestChecksV1(host Host, probe api.CapProbe, system, architecture string) []Check {
	if host.Machine == nil || host.Machine.Provider != "lima" || system != "darwin" || architecture != "arm64" ||
		probe.OS != "linux" || probe.Arch != "arm64" || probe.KVM.Present || probe.KVM.Usable {
		return nil
	}
	return []Check{{Name: "Lima nested virtualisation", Detail: "This Lima guest has no /dev/kvm. Nested virtualisation needs an M3 or later Mac with macOS 15 or later.",
		Fix: "Check Lima's vz backend and nestedVirtualization setting. Then run the guest sandbox probe again. An unsupported Mac can still use another available wall."}}
}
