package client

import "github.com/iamlakshya/simstim/internal/api"

// hostRuleVirtualisationChecks adds advice to an actual fresh doctor probe.
// Its observations remain display metadata. They cannot raise a wall level,
// grant root work or replace the native sandbox probe at a real start.
func hostRuleVirtualisationChecks(probe api.CapProbe) []Check {
	if probe.OS != "linux" || probe.Arch != "amd64" || probe.KVM.Present || probe.CPUVirt != "" {
		return nil
	}
	check := Check{Name: "virtualisation CPU", Detail: "The host cannot see /dev/kvm or CPU virtualisation flags.",
		Fix: "If this is a VM, give it the host CPU type. On Proxmox: qm set <vmid> --cpu host"}
	if probe.Virt == "lxc" || probe.Virt == "docker" || probe.Virt == "container" {
		check.Detail = "This container shares the host kernel and cannot see /dev/kvm."
		check.Fix = "Run the helper in a VM. Do not make the container privileged or pass /dev/kvm into it."
	}
	return []Check{check}
}
