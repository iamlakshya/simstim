package host

import (
	"fmt"

	"github.com/iamlakshya/simstim/internal/wall"
)

// SetVMReferences updates retention metadata from the factory's validated
// fresh client plan. A supervisor calls it while holding its manifest lock,
// then saves that same live manifest while the driver's image lease is held.
// The metadata never supplies execution pins to a client or an Open call.
func SetVMReferences(manifest *Manifest, spec wall.Spec) error {
	if manifest == nil || spec.VM == nil || manifest.ID != spec.ID || !validID(spec.ID) {
		return fmt.Errorf("VM references name another trusted session")
	}
	manifest.Run.Image = spec.VM.Image.PlatformDigest
	manifest.Run.Label = spec.VM.Runtime.Runtime
	manifest.Run.Artefacts = manifest.Run.Artefacts[:0]
	for _, item := range spec.VM.Runtime.Items {
		manifest.Run.Artefacts = append(manifest.Run.Artefacts, item.SHA256)
	}
	return nil
}
