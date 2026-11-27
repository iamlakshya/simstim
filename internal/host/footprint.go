package host

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/paths"
)

// RecordFootprint records advisory intent only. Native file ownership can be
// attached only by the actual private producer while it holds its descriptor.
// In particular a supplied path/ref cannot issue cleanup or command authority.
func RecordFootprint(change Footprint) error {
	return RecordFootprintContext(context.Background(), change)
}

func RecordFootprintContext(ctx context.Context, change Footprint) (returned error) {
	if change.Owned != nil || change.Original != nil || change.Directory != nil || change.PreparedRef != nil || change.Prepared != "" || change.Quarantine != "" || change.Link != nil || len(change.Pending) != 0 || len(change.extra) != 0 {
		return placementError("a footprint description cannot supply native producer receipts")
	}
	if !knownFootprintKindV1(change.Kind) || change.Path != "" || change.Root != "" || change.Status != "" {
		return placementError("the advisory footprint has an unknown fixed role")
	}
	owner, err := beginStateMetadataPublisher(ctx)
	if err != nil {
		return stateMetadataFailure(err)
	}
	defer func() { returned = errors.Join(returned, owner.close()) }()
	change.ID, err = footprintNonce()
	if err != nil {
		return err
	}
	change.At, change.Status = time.Now().UTC(), "intent"
	return withFootprintControlV1(owner, func(control *placementControl, guard *os.Root) error {
		control.Footprints = append(control.Footprints, change)
		return writeFootprintArchiveV1(owner.ctx, guard, control)
	})
}

func knownFootprintKindV1(kind string) bool {
	return kind == "link" || kind == "unit" || kind == "terminfo" || kind == "linger" || kind == "slice" || kind == "directory"
}

func footprintRolePathV1(role string) (string, error) {
	roles, err := baselineParentPaths()
	if err != nil {
		return "", err
	}
	if role == "slice_control" {
		return filepath.Join(roles["systemd"], "user.control", brand.Slice+".d"), nil
	}
	if role == "parent:user_control" {
		return roles["systemd"], nil
	}
	if role == "parent:slice_dropin" {
		return filepath.Join(roles["systemd"], "user.control"), nil
	}
	if bucket, ok := strings.CutPrefix(role, "terminfo:"); ok && validTerminfoBucketV1(bucket) {
		return filepath.Join(roles["terminfo"], bucket), nil
	}
	if bucket, ok := strings.CutPrefix(role, "parent:terminfo_bucket:"); ok && validTerminfoBucketV1(bucket) {
		return roles["terminfo"], nil
	}
	if key, ok := strings.CutPrefix(role, "parent:"); ok {
		for _, known := range api.BaselineParentKeys() {
			if known == key {
				return filepath.Dir(roles[key]), nil
			}
		}
		return "", placementError("the footprint parent has an unknown role")
	}
	if role != "systemd_user" && role != "terminfo" && role != "default_wants" && role != "timers_wants" {
		return "", placementError("the footprint has an unknown outside directory role")
	}
	return roles[role], nil
}

func knownFootprintLeafV1(role, leaf string) bool {
	if role == "slice_control" {
		return leaf == "50-MemoryMax.conf"
	}
	if role == "parent:user_control" {
		return leaf == "user.control"
	}
	if role == "parent:slice_dropin" {
		return leaf == brand.Slice+".d"
	}

	if bucket, ok := strings.CutPrefix(role, "parent:terminfo_bucket:"); ok {
		return validTerminfoBucketV1(bucket) && leaf == bucket
	}
	if bucket, ok := strings.CutPrefix(role, "terminfo:"); ok {
		return validTerminfoBucketV1(bucket) && filepath.Base(leaf) == leaf && leaf != "." && leaf != ".." && !strings.ContainsAny(leaf, "\\\x00")
	}
	if role == "default_wants" {
		return leaf == restoreUnit()
	}
	if role == "timers_wants" {
		return leaf == idleUnit("timer")
	}
	if key, ok := strings.CutPrefix(role, "parent:"); ok {
		roles, err := baselineParentPaths()
		if err != nil {
			return false
		}
		for _, known := range api.BaselineParentKeys() {
			if key == known {
				return leaf == filepath.Base(roles[key])
			}
		}
		return false
	}
	if role == "systemd_user" {
		return leaf == restoreUnit() || leaf == idleUnit("service") || leaf == idleUnit("timer")
	}
	if role == "terminfo" {
		return filepath.IsLocal(leaf) && filepath.Clean(leaf) == leaf && !strings.ContainsAny(leaf, "\\\x00") && strings.Count(leaf, string(filepath.Separator)) == 1
	}
	return false
}

func validateFootprintLedgerV1(data []byte) error {
	var ledger footprintLedger
	if !api.ExactJSONFields(data, ledger) || json.Unmarshal(data, &ledger) != nil || ledger.Version != 1 || !ValidHostID(ledger.HostID) || ledger.UID != os.Getuid() || len(ledger.extra) != 0 {
		return placementError("the footprint archive has an unknown owner or schema")
	}
	seen := map[string]bool{}
	for _, change := range ledger.Entries {
		if !ValidHostID(change.ID) || change.At.IsZero() || seen[change.ID] || len(change.extra) != 0 || !knownFootprintKindV1(change.Kind) {
			return placementError("the footprint archive has an unknown or duplicate entry")
		}
		seen[change.ID] = true
		if change.Root != "" && !knownFootprintLeafV1(change.Root, change.Path) {
			return placementError("the footprint file has an unknown role")
		}
	}
	return nil
}

func withFootprintControlV1(owner *stateMetadataPublisher, change func(*placementControl, *os.Root) error) error {
	return withPlacementRoot(owner.ctx, false, func(control *placementControl, guard *os.Root) error {
		if err := checkStateMetadataPublisher(owner.ctx, control, owner.operation); err != nil {
			return err
		}
		for _, entry := range control.Footprints {
			if len(entry.extra) != 0 {
				return placementError("future footprint entries grant no publication authority")
			}
		}
		return change(control, guard)
	}, true)
}

// The retained outside guard is canonical. This private state archive is a
// checked copy for inspection; a copied archive cannot issue removal rights.
func writeFootprintArchiveV1(ctx context.Context, guard *os.Root, control *placementControl) error {
	data, err := json.Marshal(footprintLedger{Version: 1, HostID: control.HostID, UID: control.UID, Entries: control.Footprints})
	if err != nil {
		return err
	}
	if err := validateFootprintLedgerV1(data); err != nil {
		return err
	}
	// Persist the ahead intent before changing any public outside resource.
	if err := writePlacementControl(guard, *control); err != nil {
		return err
	}
	state, err := cachefs.OpenPrivateRoot(paths.StateDir(), false)
	if err != nil {
		return err
	}
	defer state.Close()
	if err := checkStateMetadataRoot(control, state); err != nil {
		return err
	}
	if err := replayStateMetadata(ctx, guard, control, state, stateFootprint); err != nil {
		return err
	}
	_, before, err := readStateMetadata(ctx, state, stateFootprint)
	if err != nil {
		return err
	}
	key := "state/" + string(stateFootprint)
	if before != nil && !control.Refs[key].matches(before) {
		return stateMetadataPreserved(stateFootprint, "its native producer receipt changed")
	}
	after, err := writeOwnedMetadataIntent(state, string(stateFootprint), data, before, func(info os.FileInfo, temporary string) error {
		return prepareStateMetadataWrite(ctx, guard, control, state, stateFootprint, before, info, temporary)
	})
	if err != nil {
		return err
	}
	if control.Refs == nil {
		control.Refs = map[string]purgeRef{}
	}
	control.Refs[key] = refOf(after)
	delete(control.StateWrites, string(stateFootprint))
	return nil
}

func footprintDisplayPathV1(role, leaf string) string {
	if role == "slice_control" {
		return "~/.config/systemd/user.control/" + brand.Slice + ".d/" + leaf
	}
	if role == "default_wants" || role == "timers_wants" {
		target := "default.target.wants"
		if role == "timers_wants" {
			target = "timers.target.wants"
		}
		return "~/.config/systemd/user/" + target + "/" + leaf
	}
	if bucket, ok := strings.CutPrefix(role, "terminfo:"); ok {
		return "~/.terminfo/" + bucket + "/" + leaf
	}

	if role == "systemd_user" {
		return "~/.config/systemd/user/" + leaf
	}
	if role == "terminfo" {
		return "~/.terminfo/" + leaf
	}
	return brand.Name + " managed setting"
}

func validTerminfoBucketV1(bucket string) bool {
	if len(bucket) == 1 {
		c := bucket[0]
		return c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z'
	}
	return len(bucket) == 2 && strings.Trim(bucket, "0123456789abcdef") == ""
}
