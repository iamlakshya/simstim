package client

import (
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
)

// RootPackageNotes names missing tools without offering package installation as
// a root step. The distribution text is only compared and never run as code.
func RootPackageNotes(p api.CapProbe) []Detail {
	if p.OS != "linux" {
		return nil
	}
	var packages []string
	if !p.HasSetfacl {
		packages = append(packages, "acl (setfacl)")
	}
	if !p.HasNewuidmap {
		packages = append(packages, "uidmap (newuidmap and newgidmap)")
	}
	if len(packages) == 0 {
		return nil
	}
	out := []Detail{{Label: "Packages", Value: "missing " + strings.Join(packages, ", ")}}
	distro := strings.ToLower(p.Distro)
	if strings.Contains(distro, "ubuntu") || strings.Contains(distro, "debian") {
		out = append(out, Detail{Label: "Try", Value: "sudo apt install acl uidmap on the host, then run host prepare again"})
	} else {
		out = append(out, Detail{Label: "Try", Value: "Ask the machine's owner to install acl and uidmap, then run host prepare again."})
	}
	return out
}
