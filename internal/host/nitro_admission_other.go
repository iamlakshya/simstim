//go:build !linux

package host

import "context"

func checkNitroAdmissionLockedV1(_ context.Context, _ *SessionOperation, m *Manifest) error {
	if m != nil && m.Wall != nil && m.Wall.Driver == "nitro" {
		return UserError("wall_unavailable", "Nitro Sandboxes need a Linux parent", "Use a supported Linux EC2 host.")
	}
	return nil
}
