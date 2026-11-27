package host

import "runtime"

func validateRestartWall(m *Manifest) error {
	if m == nil {
		return sessionLeaseError("restart has no protected session")
	}
	if m.Wall == nil {
		return checkManifestUnwalled(m)
	}
	if runtime.GOOS != "linux" && m.Wall.Driver != "plain" {
		return UserError("wall_unavailable", "this recorded sandbox needs a Linux native startup owner", "Choose a supported Linux host for this sandbox; its recorded wall has been kept.")
	}
	switch m.Wall.Driver {
	case "none":
		if !IsNoneSupervisor(m) || m.Files != nil || len(m.VMNeedV1) != 0 || m.RuntimeManifest != nil {
			return vmNeedError("the recorded unwalled supervisor has conflicting sandbox inputs")
		}
	case "process", "gvisor":
		if m.Wall.Level != m.Wall.Driver {
			return vmNeedError("the recorded wall level and driver conflict")
		}
	case "chv", "firecracker", "nitro":
		if m.Wall.Level != "microvm" || len(m.VMNeedV1) == 0 {
			return vmNeedError("the recorded microvm has no matching original Need")
		}
	case "plain":
		if m.Wall.Level != "none" {
			return vmNeedError("the explicit plain test driver cannot claim a production wall")
		}
	default:
		return UserError("wall_unsupported", "this helper has no actual driver for the recorded wall", "Use a helper with this wall's complete driver; its recorded wall has been kept.")
	}
	return ValidateManifestStart(m)
}
