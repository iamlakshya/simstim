package supply

import (
	"slices"
	"strings"

	"github.com/iamlakshya/simstim/internal/brand"
)

// Arches are the host architectures that a manifest may name.
var Arches = []string{"amd64", "arm64"}

// unameNames is what `uname -m` prints on each architecture. It is the one
// table of these names: Machines and ArchOf read it.
var unameNames = map[string][]string{"amd64": {"x86_64", "amd64"}, "arm64": {"aarch64", "arm64"}}

// Machines is what `uname -m` prints on an architecture of Arches.
func Machines(arch string) []string { return slices.Clone(unameNames[arch]) }

// ArchOf is the architecture of a `uname -m` answer. An answer that names
// no architecture of Arches gives itself, and false.
func ArchOf(machine string) (string, bool) {
	for _, a := range Arches {
		if slices.Contains(unameNames[a], machine) {
			return a, true
		}
	}
	return machine, false
}

// Carried is a kind of file that a client build carries and installs on
// hosts. The build keeps it as Prefix plus the architecture, such as
// host-linux-amd64. Its asset on the release is AssetName.
type Carried struct {
	Kind, Name, Prefix string
}

// CarriedFiles is the one list of the files that a build carries: the host
// helper and the shadow terminal. The manifest writer and the client read it.
var CarriedFiles = []Carried{
	{KindHelper, brand.HostBinary, "host-linux-"},
	{KindVT, brand.VTBinary, "vt-linux-"},
}

// File is the name of the build's file for an architecture.
func (c Carried) File(arch string) string { return c.Prefix + arch }

// AssetName is the name of the file on the release, such as
// <name>-host-linux-amd64.
func (c Carried) AssetName(arch string) string {
	return brand.Name + "-" + strings.TrimSuffix(c.Prefix, "-") + "-" + arch
}

// KernelAsset is the release asset name of the guest kernel for arch
// (isolation spec 20.1): a PVH ELF on amd64, and an uncompressed Image on arm64.
func KernelAsset(arch string) string {
	if arch == "arm64" {
		return "Image-arm64"
	}
	return "vmlinux-" + arch
}

// ToolsAsset is the release asset name of the tools EROFS image for arch.
func ToolsAsset(arch string) string {
	return "tools-" + arch + ".erofs"
}

// ConfigAsset is the release asset name of the guest kernel .config for arch.
func ConfigAsset(arch string) string {
	return "config-" + arch
}

// KernelSourceAsset is the release asset name of the upstream Linux tarball.
func KernelSourceAsset(version string) string {
	return "linux-" + version + ".tar.xz"
}
