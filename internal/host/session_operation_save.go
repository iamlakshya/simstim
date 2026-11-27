package host

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"time"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

// saveNewManifest uses the original reserved directory descriptor and the one
// canonical manifest codec. A moved or replaced path can neither redirect the
// write nor turn an existing manifest into a newly admitted primary.
func (operation *SessionOperation) saveNewManifest(m *Manifest) error {
	return operation.saveNewManifestGuardedV1(m, nil)
}

// The actual original writer may retain a private bounded pre-Rename check.
// A callback carries no producer authority; its actual caller supplies owners.
func (operation *SessionOperation) saveNewManifestGuardedV1(m *Manifest, preRename func(context.Context) error) error {
	if operation == nil || m == nil || m.ID != operation.id || m.path != "" || m.SandboxOf != "" || operation.native == nil || operation.native.borrowed {
		return sessionLeaseError("the new manifest has no original owning reservation")
	}
	return operation.withCurrentRead(operation.ownerContext, func(ctx context.Context) (failure error) {
		m.UpdatedAt = time.Now().UTC()
		raw, err := m.MarshalJSON()
		if err != nil {
			return err
		}
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return err
		}
		name := ".manifest-" + hex.EncodeToString(random[:])
		file, err := operation.native.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		published := false
		defer func() {
			if !published {
				failure = errors.Join(failure, operation.native.root.Remove(name))
			}
		}()
		if _, err := file.Write(append(raw, '\n')); err != nil {
			return errors.Join(err, file.Close())
		}
		if err := file.Sync(); err != nil {
			return errors.Join(err, file.Close())
		}
		if err := closeManifestTemp(file); err != nil {
			return errors.Join(err, file.Close())
		}
		if preRename != nil {
			if err := preRename(ctx); err != nil {
				return err
			}
		}
		if err := operation.native.check(ctx); err != nil {
			return err
		}
		if err := cachefs.RenameNoReplace(operation.native.root, name, "manifest.json"); err != nil {
			return err
		}
		published = true
		if err := syncCapturedDirectory(operation.native.root); err != nil {
			return err
		}
		return operation.native.check(ctx)
	})
}
