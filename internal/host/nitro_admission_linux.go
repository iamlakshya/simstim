//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"runtime"
	"time"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/nitrodev"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/vmneed"
)

// checkNitroAdmissionLockedV1 runs inside the SAME placement transaction as
// ordinary memory admission. Only original protected manifests reserve. Native
// counters cover non-managed enclaves and no second reservation store is made.
func checkNitroAdmissionLockedV1(ctx context.Context, operation *SessionOperation, proposed *Manifest) error {
	if proposed == nil || proposed.Wall == nil || proposed.Wall.Driver != "nitro" {
		return nil
	}
	if operation == nil || !operation.Valid(operation.Primary()) || operation.native == nil || operation.native.borrowed || proposed.ID != operation.Primary() {
		return sessionLeaseError("Nitro admission has no original owning operation")
	}
	need, err := vmneed.DecodeV1(proposed.VMNeedV1, runtime.GOARCH)
	if err != nil {
		return err
	}
	if err := need.ValidateWall(runtime.GOARCH, proposed.Wall.Level, proposed.Wall.Driver, proposed.Limits.MemoryMax); err != nil {
		return err
	}
	resources, err := nitrodev.ReadResources()
	if err != nil {
		return nitroRoomErrorV1(resources, nitrodev.Reservation{}, err)
	}
	var used nitrodev.Reservation
	root, err := cachefs.OpenPrivateRoot(paths.SessionsDir(), false)
	if err != nil {
		return err
	}
	defer root.Close()
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return err
	}
	now, boot := time.Now(), BootID()
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		id := entry.Name()
		if id == proposed.ID {
			continue
		}
		m, err := readRuntimeReference(root, id)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if m.SandboxOf != "" || m.Run.BootID != boot || m.Wall == nil || m.Wall.Driver != "nitro" {
			continue
		}
		unit, err := readMemoryUnitV1(ctx, id, true)
		if err != nil {
			return err
		}
		starting := m.Status.State == StateStarting && (m.Status.Since.IsZero() || now.Sub(m.Status.Since) <= 2*time.Minute)
		preparing := m.ResumePreparation != nil || m.Auxiliary != nil
		if !unit.exists && !starting && !preparing {
			continue
		}
		other, err := vmneed.DecodeV1(m.VMNeedV1, runtime.GOARCH)
		if err != nil {
			return err
		}
		if other.Driver != "nitro" || other.ValidateWall(runtime.GOARCH, m.Wall.Level, m.Wall.Driver, m.Limits.MemoryMax) != nil {
			return nitroRoomErrorV1(resources, used, errors.New("a protected Nitro reservation is inconsistent"))
		}
		used.CPUs += int64(other.CPUs)
		if starting || preparing || !unit.exists {
			used.PendingSlots++
			used.PendingMemory += m.Limits.MemoryMax
		} else {
			used.ManagedSlots++
		}
	}
	if err := resources.Check(int64(need.CPUs), proposed.Limits.MemoryMax, used); err != nil {
		return nitroRoomErrorV1(resources, used, err)
	}
	return nil
}

func nitroRoomErrorV1(resources nitrodev.Resources, used nitrodev.Reservation, cause error) error {
	failure := UserError("no_room", cause.Error(), "Stop another sandbox, choose a smaller explicit size, or ask the owner to prepare more Nitro slots or memory.")
	failure.Data, _ = json.Marshal(struct {
		Resources nitrodev.Resources   `json:"nitro"`
		Reserved  nitrodev.Reservation `json:"reserved"`
	}{resources, used})
	return failure
}
