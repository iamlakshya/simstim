package host

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"time"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

type memoryUnitFactsV1 struct {
	exists  bool
	current int64
	maximum int64
	swap    int64
}

type memoryRoomFactsV1 struct {
	Known     bool      `json:"accounting_known"`
	BootID    string    `json:"boot_id"`
	At        time.Time `json:"at"`
	Budget    int64     `json:"budget"`
	Reserved  int64     `json:"reserved"`
	Room      int64     `json:"room"`
	Sandboxes int       `json:"sandboxes"`
	Needed    int64     `json:"needed"`
}

func noMemoryRoomV1(facts memoryRoomFactsV1) error {
	err := UserError("no_room", "the complete declared sandbox size does not fit the host's current memory room", "Choose a smaller explicit sandbox size, stop another sandbox, or resize the host.")
	err.Data, _ = json.Marshal(facts)
	return err
}

func attachMemoryFactsV1(err error, needed int64) error {
	var supplied *proto.Error
	if err != nil && errors.As(err, &supplied) && supplied.Code == "no_room" && len(supplied.Data) == 0 {
		facts := memoryRoomFactsV1{BootID: BootID(), At: time.Now().UTC(), Budget: DefaultMemoryMax(), Needed: needed}
		supplied.Data, _ = json.Marshal(facts)
	}
	return err
}

func declaredUnitMemoryV1(m *Manifest, native memoryUnitFactsV1) (int64, error) {
	if m.Limits.MemoryMax <= 0 || m.Limits.Declared < 0 || m.Limits.Declared != 0 && m.Limits.Declared != m.Limits.MemoryMax {
		return 0, UserError("no_room", "a protected sandbox reservation has inconsistent complete unit memory", "Inspect or restart that sandbox through its original owner before admitting another.")
	}
	// A pre-declared-field record still reserves its complete known cap. It
	// cannot turn into zero, and an existing unit must prove that exact cap.
	declared := m.Limits.MemoryMax
	if native.exists && native.maximum != declared {
		return 0, UserError("no_room", "a sandbox's native cgroup cap differs from its protected reservation", "Retain the sandbox and inspect its actual memory cap before starting another.")
	}
	if err := ValidateManifestStart(m); err != nil {
		return 0, err
	}
	return declared, nil
}

func reservationChargeV1(m *Manifest, boot string, now time.Time, native memoryUnitFactsV1) (int64, error) {
	if m.SandboxOf != "" || m.Run.BootID != boot {
		return 0, nil
	}
	starting := m.Status.State == StateStarting && (m.Status.Since.IsZero() || now.Sub(m.Status.Since) <= 2*time.Minute)
	if !native.exists && !starting {
		return 0, nil
	}
	if isWalledMemoryV1(m) {
		return declaredUnitMemoryV1(m, native)
	}
	if native.exists {
		if native.current < 0 {
			return 0, UserError("no_room", "the native session memory measurement is unknown", "Inspect the host cgroup memory controller before starting another sandbox.")
		}
		return native.current, nil
	}
	return 0, nil
}

// memoryRoomLockedV1 is called only inside the existing placement transaction.
// Protected manifests are the reservations. There is no second store or lock.
// Exclusion belongs only to the same original new/vacant restart operation.
func memoryRoomLockedV1(ctx context.Context, operation *SessionOperation, needed int64) (facts memoryRoomFactsV1, failure error) {
	facts = memoryRoomFactsV1{BootID: BootID(), At: time.Now().UTC(), Budget: DefaultMemoryMax(), Needed: needed}
	defer func() {
		var supplied *proto.Error
		if failure != nil && errors.As(failure, &supplied) && supplied.Code == "no_room" {
			supplied.Data, _ = json.Marshal(facts)
		}
	}()
	if operation == nil || !operation.Valid(operation.Primary()) || operation.native.borrowed || facts.BootID == "" || facts.Budget <= 0 || needed <= 0 {
		return facts, UserError("no_room", "the original operation or fresh host memory budget is unavailable", "Inspect this host's memory and original session operation before trying again.")
	}
	observed, err := observeMemoryRoomLockedV1(ctx, operation.Primary(), needed)
	facts = observed
	if err != nil {
		return facts, err
	}
	if needed > facts.Room {
		return facts, noMemoryRoomV1(facts)
	}
	return facts, nil
}

// observeMemoryRoomLockedV1 is the SAME read-only native accounting scan used
// by original admission and host.facts. Diagnostic calls use excluded="" and
// needed=0; they cannot create an operation, reservation or exclusion grant.
func observeMemoryRoomLockedV1(ctx context.Context, excluded string, needed int64) (facts memoryRoomFactsV1, failure error) {
	facts = memoryRoomFactsV1{BootID: BootID(), At: time.Now().UTC(), Budget: DefaultMemoryMax(), Needed: needed}
	if facts.BootID == "" || facts.Budget <= 0 {
		return facts, UserError("no_room", "the current native memory budget or boot identity is unknown", "Inspect the host memory controller before using its room.")
	}
	units, err := memoryUnitIDsV1(ctx)
	if err != nil {
		return facts, err
	}
	unitsLeft := make(map[string]bool, len(units))
	for _, id := range units {
		if id != excluded {
			unitsLeft[id] = true
		}
	}
	root, err := cachefs.OpenPrivateRoot(paths.SessionsDir(), false)
	if errors.Is(err, os.ErrNotExist) && len(unitsLeft) == 0 {
		facts.Known, facts.Room = true, facts.Budget
		return facts, nil
	}
	if err != nil {
		return facts, err
	}
	defer root.Close()
	directory, err := root.Open(".")
	if err != nil {
		return facts, err
	}
	defer directory.Close()
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return facts, err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return facts, err
		}
		id := entry.Name()
		delete(unitsLeft, id)
		info, err := root.Lstat(id)
		if err != nil || !validID(id) || !info.IsDir() || !wallCacheOwned(info) || info.Mode().Perm() != 0o700 {
			return facts, errors.Join(UserError("no_room", "the protected reservation directory is uncertain", "Retain the host state and inspect its session records."), err)
		}
		if id == excluded {
			// Caller proves either first-publication absence or native holder
			// vacancy before replacing this same original reservation.
			continue
		}
		manifest, err := readRuntimeReference(root, id)
		if errors.Is(err, os.ErrNotExist) {
			// Another original New may own a reserved directory but cannot
			// launch before its atomic first manifest. An existing canonical
			// unit without that record is unknown, never zero.
			unit, nativeErr := readMemoryUnitV1(ctx, id, false)
			if nativeErr != nil || unit.exists {
				return facts, errors.Join(UserError("no_room", "a native unit has no readable protected reservation", "Inspect the host's current session accounting."), nativeErr)
			}
			continue
		}
		if err != nil {
			return facts, err
		}
		if manifest.ID != id {
			return facts, UserError("no_room", "a protected reservation names another primary", "Inspect this host's protected session records before starting another sandbox.")
		}
		if manifest.SandboxOf != "" || manifest.Run.BootID != facts.BootID {
			continue
		}
		unit, err := readMemoryUnitV1(ctx, id, isWalledMemoryV1(&manifest))
		if err != nil {
			return facts, err
		}
		charge, err := reservationChargeV1(&manifest, facts.BootID, facts.At, unit)
		if err != nil {
			return facts, err
		}
		if charge < 0 || facts.Reserved > math.MaxInt64-charge {
			return facts, UserError("no_room", "the native reservation total cannot be represented", "Inspect the host's current session memory records.")
		}
		facts.Reserved += charge
		if manifest.Wall != nil && (unit.exists || manifest.Status.State == StateStarting && (manifest.Status.Since.IsZero() || facts.At.Sub(manifest.Status.Since) <= 2*time.Minute)) {
			facts.Sandboxes++
		}
	}
	if len(unitsLeft) != 0 {
		return facts, UserError("no_room", "a canonical native unit has no protected reservation directory", "Inspect the host's native session units and preserve their current reservations.")
	}
	facts.Known = true
	facts.Room = facts.Budget - facts.Reserved
	return facts, nil
}

func admitNewMemoryLockedV1(ctx context.Context, operation *SessionOperation, m *Manifest) error {
	if !isWalledMemoryV1(m) {
		return nil
	}
	if m.Limits.Declared != m.Limits.MemoryMax {
		return newRuntimeError("the new sandbox understates its complete declared unit cap")
	}
	if err := checkExactWalledCapV1(m.Limits.MemoryMax); err != nil {
		return err
	}
	if _, err := operation.native.root.Lstat("manifest.json"); !errors.Is(err, os.ErrNotExist) {
		return errors.Join(sessionLeaseError("a first reservation cannot replace an existing protected primary"), err)
	}
	unit, err := readMemoryUnitV1(ctx, m.ID, true)
	if err != nil {
		return attachMemoryFactsV1(err, m.Limits.Declared)
	}
	if unit.exists {
		return sessionLeaseError("the original new reservation already has a native unit")
	}
	if _, err := memoryRoomLockedV1(ctx, operation, m.Limits.Declared); err != nil {
		return err
	}
	m.Run.BootID = BootID()
	m.Status.State, m.Status.Since = StateStarting, time.Now().UTC()
	return nil
}
