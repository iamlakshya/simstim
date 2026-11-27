package host

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

type sandboxMemberIdentity struct {
	ID      string
	Parent  string
	Created time.Time
}
type sandboxRunIdentity struct {
	Version                           int
	Primary                           sandboxMemberIdentity
	Level, Driver, Generation, BootID string
	Started                           time.Time
	Members                           []sandboxMemberIdentity
}

// SandboxSnapshot reads protected structural facts under the canonical primary
// operation lease. Actual running helper capabilities are checked separately.
func SandboxSnapshot(id string) (*api.SandboxSnapshot, error) {
	primary, err := SandboxPrimaryID(id)
	if err != nil {
		return nil, err
	}
	var result *api.SandboxSnapshot
	err = WithSessionLease(primary, supervisorSnapshotWait, func() error { var err error; result, err = SandboxSnapshotLocked(id, primary); return err })
	return result, err
}

// SandboxSnapshotLocked requires the caller to hold primary op.lock through
// its subsequent comparison and any member/value/state publication.
func SandboxSnapshotLocked(original, primary string) (*api.SandboxSnapshot, error) {
	actual, err := SandboxPrimaryID(original)
	if err != nil {
		return nil, err
	}
	if actual != primary {
		return nil, snapshotChanged()
	}
	root, err := LoadManifest(primary)
	if err != nil {
		return nil, err
	}
	if root.ID != primary || root.Wall == nil {
		return nil, UserError("not_walled", "the session has no sandbox", "Start a walled session before joining it.")
	}
	children, err := SandboxMembers(primary)
	if err != nil {
		return nil, err
	}
	return snapshotFromProtected(root, children, original)
}

// snapshotFromProtected is the SAME snapshot codec for a staged protected
// creation/restore record before publication under its retained operation.
func snapshotFromProtected(root *Manifest, children []*Manifest, original string) (*api.SandboxSnapshot, error) {
	primary := root.ID
	children = slices.Clone(children)
	slices.SortFunc(children, func(a, b *Manifest) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	identity := sandboxRunIdentity{Version: 1, Primary: sandboxMemberIdentity{ID: primary, Created: root.CreatedAt.UTC()}, Level: root.Wall.Level, Driver: root.Wall.Driver, Generation: root.Run.Generation, BootID: root.Run.BootID, Started: root.Run.StartedAt.UTC(), Members: []sandboxMemberIdentity{}}
	snapshot := &api.SandboxSnapshot{Version: 1, Primary: primary, Members: []string{}}
	for _, child := range children {
		snapshot.Members = append(snapshot.Members, child.ID)
		identity.Members = append(identity.Members, sandboxMemberIdentity{ID: child.ID, Parent: child.SandboxOf, Created: child.CreatedAt.UTC()})
	}
	encoded, err := json.Marshal(identity)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(append([]byte("sandbox-snapshot-v1\n"), encoded...))
	snapshot.Generation = hex.EncodeToString(sum[:])
	if err := snapshot.Check(original, proto.MaxFrame); err != nil {
		return nil, err
	}
	return snapshot, nil
}

// CheckSandboxSnapshotLocked re-reads the complete protected identity while
// the same primary operation lease still excludes spawn, resume and deletion.
func CheckSandboxSnapshotLocked(original, primary string, declared *api.SandboxSnapshot) error {
	if err := declared.Check(original, proto.MaxFrame); err != nil {
		return snapshotChanged()
	}
	fresh, err := SandboxSnapshotLocked(original, primary)
	if err != nil {
		return err
	}
	if declared.Primary != fresh.Primary || declared.Version != fresh.Version || declared.Generation != fresh.Generation || !slices.Equal(declared.Members, fresh.Members) {
		return snapshotChanged()
	}
	return nil
}
func snapshotChanged() error {
	return UserError("sandbox_changed", "the sandbox's identity or members changed", "Read its complete snapshot again before continuing.")
}

const supervisorSnapshotWait = 5 * time.Second
