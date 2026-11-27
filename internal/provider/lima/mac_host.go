package lima

import "github.com/iamlakshya/simstim/internal/limaprofile"

// MacHostProfileV1 is the closed original join projection for a plain Mac VM.
type MacHostProfileV1 = limaprofile.MacHostProfileV1

// MacHostYAMLV1 renders the shared fixed profile without provider execution.
func MacHostYAMLV1(profile MacHostProfileV1) ([]byte, error) {
	return limaprofile.MacHostYAMLV1(profile)
}
