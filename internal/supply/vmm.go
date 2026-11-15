package supply

import "fmt"

// CloudHypervisorName is the runtime name of the microvm VMM in supply.lock.
const CloudHypervisorName = "cloud-hypervisor"

// FirecrackerName is the runtime name shared by the Firecracker drivers.
const FirecrackerName = "firecracker"

// CloudHypervisorHostKernel is this project's tested-support floor. It is
// release metadata, not a claim that an untested host can construct the wall.
const CloudHypervisorHostKernel = ">=6.8"

// FirecrackerHostKernel lists the upstream-tested host kernel lines. Runtime
// admission also checks the page size and device access.
const FirecrackerHostKernel = "5.10,6.1,6.18"

// VMMFile selects a binary or source pin from an already validated lock table.
// It never obtains authority from downloaded bytes or a host inventory.
func VMMFile(v LockVMM, target string) (LockFile, error) {
	switch target {
	case "amd64":
		return v.AMD64, nil
	case "arm64":
		return v.ARM64, nil
	case "source":
		return v.Source, nil
	default:
		return LockFile{}, fmt.Errorf("unknown VMM target %q; use amd64, arm64 or source", target)
	}
}

// VMMAssetName names the unchanged upstream artefact mirrored by the release.
// A new runtime family needs its own reviewed naming/support policy.
func VMMAssetName(v LockVMM, target string) (string, error) {
	if !cleanVersion(v.Version) {
		return "", fmt.Errorf("VMM version %q is not a clean release version", v.Version)
	}
	if v.Name == FirecrackerName {
		switch target {
		case "amd64":
			return "firecracker-v" + v.Version + "-x86_64", nil
		case "arm64":
			return "firecracker-v" + v.Version + "-aarch64", nil
		case "source":
			return "firecracker-v" + v.Version + ".tar.gz", nil
		default:
			return "", fmt.Errorf("unknown VMM target %q; use amd64, arm64 or source", target)
		}
	}
	if v.Name != CloudHypervisorName {
		return "", fmt.Errorf("no release asset policy for VMM %q", v.Name)
	}
	switch target {
	case "amd64":
		return "cloud-hypervisor-static", nil
	case "arm64":
		return "cloud-hypervisor-static-aarch64", nil
	case "source":
		return "cloud-hypervisor-v" + v.Version + ".tar.xz", nil
	default:
		return "", fmt.Errorf("unknown VMM target %q; use amd64, arm64 or source", target)
	}
}

// ReleaseVMMItem constructs one client-authoritative release entry from an
// already validated lock table. Callers must CheckFile the actual mirrored
// binary and verify the source pin before appending it to a release manifest.
func ReleaseVMMItem(release string, v LockVMM, arch string) (Item, error) {
	if v.Name == FirecrackerName {
		return Item{}, fmt.Errorf("Firecracker pins an archive; mirror and hash its extracted runtime file before writing the release item")
	}
	return releaseVMMItem(release, v, arch, vmmPin(v, arch))
}

func vmmPin(v LockVMM, arch string) LockFile {
	if arch == "arm64" {
		return v.ARM64
	}
	return v.AMD64
}

func releaseVMMItem(release string, v LockVMM, arch string, pin LockFile) (Item, error) {
	if arch != "amd64" && arch != "arm64" {
		return Item{}, fmt.Errorf("VMM architecture %q must be amd64 or arm64", arch)
	}
	asset, err := VMMAssetName(v, arch)
	if err != nil {
		return Item{}, err
	}
	source, err := VMMAssetName(v, "source")
	if err != nil {
		return Item{}, err
	}
	item := Item{Kind: KindVMM, Name: v.Name, Version: v.Version, Arch: arch, SHA256: pin.SHA256, Size: pin.Size, URL: ReleaseFolder(release) + "/" + asset, SourceURL: ReleaseFolder(release) + "/" + source, Licence: "Apache-2.0 AND BSD-3-Clause", Released: v.ReleasedDate(), EOL: v.EOLDate(), HostKernel: CloudHypervisorHostKernel}
	if v.Name == FirecrackerName {
		if Below(v.Version, "1.18.0") {
			return Item{}, fmt.Errorf("Firecracker release pin %s is below 1.18.0", v.Version)
		}
		item.Licence, item.HostKernel = "Apache-2.0", FirecrackerHostKernel
	}
	if err := (&Manifest{Release: release, Items: []Item{item}}).Validate(); err != nil {
		return Item{}, fmt.Errorf("validate VMM release item: %w", err)
	}
	return item, nil
}
