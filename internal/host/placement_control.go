package host

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

var errPlacementNoChange = errors.New("placement control unchanged")

type placementOperationRecord struct {
	PID   int       `json:"pid"`
	Birth string    `json:"birth"`
	Kind  string    `json:"kind"`
	At    time.Time `json:"at"`
	extra string
}

type placementControl struct {
	ScopeFormat       int                                 `json:"scope_format"`
	PersistentRoots   persistentRootBinding               `json:"persistent_roots"`
	RuntimeRoots      map[string]runtimeRootReceipt       `json:"runtime_roots,omitempty"`
	Version           int                                 `json:"version"`
	Scope             string                              `json:"scope"`
	Control           purgeRef                            `json:"control_generation"`
	ControlLock       purgeRef                            `json:"control_lock_generation"`
	UID               int                                 `json:"uid"`
	HostID            string                              `json:"host_id"`
	State             purgeRef                            `json:"state_generation"`
	Host              purgeRef                            `json:"host_generation"`
	Place             purgeRef                            `json:"place_generation"`
	Drain             string                              `json:"drain,omitempty"`
	Nonce             string                              `json:"operation_nonce,omitempty"`
	Active            map[string]placementOperationRecord `json:"active"`
	Report            *api.PurgeResult                    `json:"report,omitempty"`
	Requested         []string                            `json:"requested,omitempty"`
	Completed         []string                            `json:"completed,omitempty"`
	BaselineBound     bool                                `json:"baseline_bound"`
	BaselineAbsent    []api.BaselineParentClaim           `json:"baseline_absent,omitempty"`
	Parents           map[string]purgeRef                 `json:"parents,omitempty"`
	Phase             string                              `json:"phase,omitempty"`
	Destructive       bool                                `json:"destructive"`
	OwnerRun          string                              `json:"owner_run,omitempty"`
	Owner             *placementOperationRecord           `json:"owner,omitempty"`
	Roots             map[string]purgeRef                 `json:"roots,omitempty"`
	Pending           *purgeResource                      `json:"pending,omitempty"`
	Sessions          map[string]purgeSessionPlan         `json:"sessions,omitempty"`
	CleanupPayloads   map[string]cleanupPayloadIntent     `json:"cleanup_payloads,omitempty"`
	LoginSnapshot     bool                                `json:"login_snapshot"`
	LoginComplete     bool                                `json:"login_complete"`
	LoginDelete       *bool                               `json:"login_delete,omitempty"`
	RuntimeTerminal   bool                                `json:"runtime_terminal"`
	SessionsCollected bool                                `json:"sessions_collected"`
	Archives          map[string]purgeRef                 `json:"archives,omitempty"`
	FinalCaptures     map[string]terminalCapture          `json:"final_captures,omitempty"`
	GuardWitness      *api.PurgeGuardWitness              `json:"guard_witness,omitempty"`
	ShutdownCommitted *idleShutdownCommit                 `json:"shutdown_committed,omitempty"`
	StateWrites       map[string]metadataWriteIntent      `json:"state_writes,omitempty"`
	ArchiveWrites     map[string]metadataWriteIntent      `json:"archive_writes,omitempty"`
	Footprints        []Footprint                         `json:"footprints,omitempty"`
	Refs              map[string]purgeRef                 `json:"refs,omitempty"`
	extra             map[string]json.RawMessage
}

func readPlacementControl(root *os.Root, scope string) (placementControl, bool, error) {
	var control placementControl
	file, err := cachefs.OpenFile(root, "control.json", os.O_RDONLY, 0o600)
	if errors.Is(err, os.ErrNotExist) {
		return control, false, nil
	}
	if err != nil {
		return control, false, placementError("cannot read the retained purge control: " + err.Error())
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, int64(proto.MaxFrame)+1))
	if err != nil || len(data) > proto.MaxFrame || json.Unmarshal(data, &control) != nil {
		return control, false, placementError("the retained purge control is unreadable")
	}
	if len(control.extra) != 0 {
		return control, false, placementError("future purge-control fields grant no mutation authority")
	}
	if err := validatePlacementControlData(control, scope); err != nil {
		return control, false, err
	}

	dir, err := root.Open(".")
	if err != nil {
		return control, false, placementError("cannot inspect the purge control generation")
	}
	info, err := dir.Stat()
	dir.Close()
	lockInfo, lockErr := root.Lstat("control.lock")
	if err != nil || !control.Control.validDirectory() || !control.Control.matches(info) || lockErr != nil || !control.ControlLock.matches(lockInfo) {
		return control, false, placementError("the retained purge control or lock changed generation")
	}
	if control.Active == nil {
		control.Active = map[string]placementOperationRecord{}
	}
	return control, true, nil
}

func writePlacementControl(root *os.Root, control placementControl) error {
	data, err := encodePlacementControl(control)
	if err != nil {
		return err
	}
	before, err := metadataBefore(root, "control.json")
	if err != nil {
		return err
	}
	_, err = writeOwnedMetadata(root, "control.json", data, before)
	return err
}
func encodePlacementControl(control placementControl) ([]byte, error) {
	data, err := json.Marshal(control)
	if err != nil || len(data)+1 > proto.MaxFrame {
		return nil, placementError("cannot encode retained control within its metadata bound")
	}
	return append(data, '\n'), nil
}

func (p *placement) withControl(ctx context.Context, change func(*placementControl) error, write bool) error {
	return p.withControlRoot(ctx, func(control *placementControl, _ *os.Root) error { return change(control) }, write)
}

func (p *placement) withControlRoot(ctx context.Context, change func(*placementControl, *os.Root) error, write bool) error {
	root, err := cachefs.OpenPrivateRoot(p.control, false)
	if err != nil {
		return placementError("the anchored purge control is missing or unsafe")
	}
	defer root.Close()
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	info, err := dir.Stat()
	dir.Close()
	if err != nil || p.controlRef == nil || info.Mode().Perm() != 0o700 || !os.SameFile(p.controlRef, info) {
		return placementError("the purge control directory changed identity")
	}
	lock, err := cachefs.OpenFile(root, "control.lock", os.O_RDWR, 0o600)
	if err != nil {
		return placementError("the purge control lock is missing or unsafe")
	}
	defer lock.Close()
	unlock, err := waitPlacementFile(ctx, root, "control.lock", lock, p.controlLockRef)
	if err != nil {
		return err
	}
	defer unlock()
	if err := checkNamedPlacementRoot(root); err != nil {
		return err
	}
	control, found, err := readPlacementControl(root, p.scope)
	if err != nil {
		return err
	}
	if !found || control.HostID != p.hostID {
		return placementError("the purge control changed host generation")
	}
	if write && control.Phase == "finalising" {
		return placementError("the prepared finalising control is frozen until its saved acknowledgement")
	}
	if err := change(&control, root); err != nil {
		if errors.Is(err, errPlacementNoChange) {
			return nil
		}
		return err
	}
	if err := checkNamedPlacementRoot(root); err != nil {
		return err
	}
	if _, err := cachefs.ValidateFile(root, "control.lock", lock, 0o600); err != nil {
		return placementError("the purge control sentinel changed before completion")
	}
	if !write {
		return nil
	}
	if len(control.extra) != 0 {
		return placementError("future purge-control metadata grants no mutation authority")
	}
	if _, err := cachefs.ValidateFile(root, "control.lock", lock, 0o600); err != nil {
		return placementError("the purge control lock changed before publication")
	}
	return writePlacementControl(root, control)
}

func waitPlacementFile(ctx context.Context, root *os.Root, name string, file *os.File, pinned os.FileInfo) (func(), error) {
	if err := checkNamedPlacementRoot(root); err != nil {
		return nil, err
	}
	before, err := cachefs.ValidateFile(root, name, file, 0o600)
	if err != nil || pinned == nil || !os.SameFile(before, pinned) {
		return nil, placementError("the placement lock changed before acquisition")
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		ok, err := cachefs.TryLock(file, cachefs.Exclusive)
		if err != nil {
			return nil, placementError("cannot acquire the placement lock")
		}
		if ok {
			if err := checkNamedPlacementRoot(root); err != nil {
				file.Close()
				return nil, err
			}
			after, err := cachefs.ValidateFile(root, name, file, 0o600)
			if err != nil || !os.SameFile(pinned, after) {
				file.Close()
				return nil, placementError("the placement lock changed during acquisition")
			}
			return func() { _ = file.Close() }, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return nil, UserError("busy", "another operation kept placement busy", "Wait for that operation to finish, then try again.")
		case <-time.After(time.Millisecond):
		}
	}
}

func withPlacement(ctx context.Context, allowDrain bool, change func(*placementControl) error, write bool) error {
	return withPlacementRoot(ctx, allowDrain, func(control *placementControl, _ *os.Root) error { return change(control) }, write)
}

func withPlacementRoot(ctx context.Context, allowDrain bool, change func(*placementControl, *os.Root) error, write bool) error {
	p, err := placementForCall()
	if err != nil {
		return err
	}
	return p.withControlRoot(ctx, func(control *placementControl, controlRoot *os.Root) error {
		hadShutdown := control.ShutdownCommitted != nil
		if err := reconcileIdleShutdownCommit(control, BootID()); err != nil {
			return err
		}
		if hadShutdown && control.ShutdownCommitted == nil {
			if err := writePlacementControl(controlRoot, *control); err != nil {
				return err
			}
		}
		if !allowDrain && control.Drain != "" {
			return UserError("draining", "this host is draining for "+control.Drain, "Wait for the removal to finish before starting another session.")
		}
		if err := p.checkIdentity(); err != nil {
			return err
		}
		root, err := cachefs.OpenPrivateRoot(paths.StateDir(), false)
		if err != nil {
			return placementError("cannot anchor the original placement generation")
		}
		defer root.Close()
		file, err := cachefs.OpenFile(root, "place.lock", os.O_RDWR, 0o600)
		if err != nil {
			return placementError("the original placement lock is missing or unsafe")
		}
		defer file.Close()
		unlock, err := waitPlacementFile(ctx, root, "place.lock", file, p.placeRef)
		if err != nil {
			return err
		}
		defer unlock()
		if err := p.checkIdentity(); err != nil {
			return err
		}
		if err := p.checkStateDomain(ctx, root, file); err != nil {
			return err
		}
		if err := change(control, controlRoot); err != nil {
			return err
		}
		if write {
			return writePlacementControl(controlRoot, *control)
		}
		return nil
	}, false)
}

func (p *placementControl) UnmarshalJSON(data []byte) error {
	type plain placementControl
	var value plain
	extra, err := decodeFootprint(data, &value, []string{"scope_format", "persistent_roots", "runtime_roots", "version", "scope", "uid", "control_generation", "control_lock_generation", "host_id", "state_generation", "host_generation", "place_generation", "drain", "operation_nonce", "active", "report", "requested", "completed", "baseline_bound", "baseline_absent", "parents", "phase", "destructive", "owner_run", "owner", "roots", "pending", "sessions", "sessions_collected", "runtime_terminal", "cleanup_payloads", "login_snapshot", "login_complete", "login_delete", "archives", "archive_writes", "state_writes", "shutdown_committed", "guard_witness", "final_captures", "footprints", "refs"})
	*p = placementControl(value)
	p.extra = extra
	return err
}
func (p placementControl) MarshalJSON() ([]byte, error) {
	type plain placementControl
	return encodeFootprint(plain(p), p.extra)
}

func checkNamedPlacementRoot(root *os.Root) error {
	anchored, err := root.Open(".")
	if err != nil {
		return err
	}
	before, err := anchored.Stat()
	anchored.Close()
	if err != nil {
		return err
	}
	current, err := cachefs.OpenPrivateRoot(root.Name(), false)
	if err != nil {
		return placementError("the named lock root disappeared or became unsafe")
	}
	defer current.Close()
	dir, err := current.Open(".")
	if err != nil {
		return err
	}
	after, err := dir.Stat()
	dir.Close()
	if err != nil || !os.SameFile(before, after) {
		return placementError("the named lock root changed while admission waited")
	}
	return nil
}

func validatePlacementControlData(control placementControl, scope string) error {
	if err := checkPersistentRoots(control); err != nil {
		return err
	}
	if control.Version != 1 || control.Scope != scope || control.UID != os.Getuid() || !ValidHostID(control.HostID) || control.Drain != "" && control.Drain != "purge" || control.Drain == "purge" && !ValidHostID(control.Nonce) {
		return placementError("the retained purge control belongs to another scope, identity or version")
	}
	for token, op := range control.Active {
		if op.extra != "" || !ValidHostID(token) || op.PID <= 0 || op.Birth == "" || op.At.IsZero() || op.Kind != "start" && op.Kind != "publish" {
			return placementError("the retained purge control has an invalid operation receipt")
		}
	}
	return validatePurgeControl(control)
}
