package client

import (
	"github.com/iamlakshya/simstim/internal/api"
)

// The probe chooses only a fixed implementation, never a release item or
// execution permission. Final candidate, floor, Need and native checks follow.
func selectVMDriverV1(probe api.CapProbe, requested, intent string) (string, error) {
	candidates := []string{"nitro", "chv", "firecracker"}
	if requested != "" {
		candidates = []string{requested}
	}
	for _, name := range candidates {
		if !api.SupportsMicroVMKernel(name, probe.Kernel) {
			continue
		}
		usable := probe.KVM.Usable
		if name == "nitro" {
			usable = probe.Nitro != nil && probe.Nitro.Usable
		}
		if !usable {
			continue
		}
		for _, p := range probe.Walls {
			if p.Driver == name && p.Level == "microvm" && (p.OK || intent == "microvm" && p.CanPrepare) {
				return name, nil
			}
		}
	}
	return "", vmPrepareUnavailable()
}
