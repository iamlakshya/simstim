package host

import (
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/supply"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

func validatePurgeControl(control placementControl) error {
	if err := validateIdleShutdownCommit(control.ShutdownCommitted); err != nil {
		return err
	}
	if !slices.Contains([]string{"", "precheck", "sessions", "footprints", "roots", "helpers", "host-complete", "finalising"}, control.Phase) {
		return placementError("the purge journal has an unknown phase")
	}
	if control.Owner != nil {
		if control.Owner.extra != "" || !ValidHostID(control.OwnerRun) || control.Owner.PID <= 0 || control.Owner.Birth == "" || control.Owner.Kind != "purge" {
			return placementError("the purge journal has an invalid process claim")
		}
	} else if control.OwnerRun != "" {
		return placementError("the purge journal has an owner token without a process")
	}
	if control.GuardWitness != nil {
		w := control.GuardWitness
		if control.Phase != "finalising" || !w.Known() || w.Marker.Size != 0 || w.MarkerSHA256 != "" || w.HostID != control.HostID || w.Nonce != control.Nonce || w.Scope != control.Scope || !samePurgeDirectoryRef(nativePurgeRef(w.Root), control.Control) || !nativeReferenceEqual(w.Sentinel, apiPurgeRef(control.ControlLock)) {
			return placementError("the prepared guard witness changed its bound generation")
		}
	}
	for name, capture := range control.FinalCaptures {
		if (name != "control.lock" && name != "footprint.json" && name != "installs.json") || len(capture.extra) != 0 || capture.Ref.Mode != 0o600 || capture.Ref.UID != uint64(os.Getuid()) || capture.Ref.Inode == 0 || len(capture.Ref.extra) != 0 || !strings.HasPrefix(capture.Quarantine, ".final-") || !ValidHostID(strings.TrimPrefix(capture.Quarantine, ".final-")) || !supply.IsHex64(capture.SHA256) {
			return placementError("the terminal capture plan is unknown")
		}
	}
	for key, receipt := range control.RuntimeRoots {
		if len(receipt.extra) != 0 || runtimeRootKey(receipt.Path) != key || !validRuntimeRootPath(receipt.Path) || (receipt.Ref.Inode != 0 && (receipt.Ref.UID != uint64(os.Getuid()) || !os.FileMode(receipt.Ref.Mode).IsDir() || len(receipt.Ref.extra) != 0)) {
			return placementError("the recorded runtime root is unknown")
		}
	}
	for key, ref := range control.Parents {
		if !slices.Contains(api.BaselineParentKeys(), key) || ref.Inode == 0 || ref.UID != uint64(os.Getuid()) || !os.FileMode(ref.Mode).IsDir() || len(ref.extra) != 0 {
			return placementError("the created parent reference is unknown")
		}
	}
	for root, ref := range control.Roots {
		if _, err := purgeRootPath(&control, root); err != nil {
			return err
		}
		if ref.Inode == 0 || ref.UID != uint64(os.Getuid()) || os.FileMode(ref.Mode).Type() != os.ModeDir || len(ref.extra) != 0 {
			return placementError("the purge root reference is unknown")
		}
	}
	if pending := control.Pending; pending != nil {
		if _, err := purgeRootPath(&control, pending.Root); err != nil {
			return err
		}
		if pending.SHA256 != "" && !supply.IsHex64(pending.SHA256) {
			return placementError("the pending cleanup digest is malformed")
		}
		if pending.Item != nil && (pending.Item.Size <= 0 || !supply.IsHex64(pending.Item.SHA256)) {
			return placementError("the pending signed cleanup metadata is malformed")
		}
		if len(pending.extra) != 0 || !validPurgeRelative(pending.Relative) || !strings.HasPrefix(pending.Quarantine, ".purge-") || !ValidHostID(strings.TrimPrefix(pending.Quarantine, ".purge-")) || pending.Ref.Inode == 0 || pending.Ref.UID != uint64(os.Getuid()) || len(pending.Ref.extra) != 0 {
			return placementError("the pending purge resource has no exact known reference")
		}
	}
	for id, plan := range control.Sessions {
		if len(plan.extra) != 0 || !validID(id) || plan.ID != id || !plan.Directory.validDirectory() {
			return placementError("the purge session plan is malformed")
		}
		if len(plan.WorktreeCapture) > 0 && !validPurgeGitTip(plan.GitTip) {
			return placementError("the captured worktree has no native branch-ref witness")
		}
		if len(plan.WorktreeCapture) > 0 && (!validPurgeRelative(plan.WorktreeCapture) || !strings.HasPrefix(filepath.Base(string(plan.WorktreeCapture)), ".purge-worktree-")) {
			return placementError("the captured worktree plan is malformed")
		}
	}
	if control.Report != nil {
		if control.Report.HostID != control.HostID || control.Report.Scope != control.Scope || control.Report.Nonce != control.Nonce || control.Report.UID != control.UID {
			return placementError("the purge report belongs to another host or generation")
		}
		if control.Report.HostComplete && !terminalStagesComplete(control.Report.Done) {
			return placementError("the host completion report lacks its complete durable phase prefix")
		}
		stages := []string{"precheck", "sessions", "footprints", "roots", "helpers", "host-complete"}
		if len(control.Report.Done) > len(stages) || !slices.Equal(control.Report.Done, stages[:len(control.Report.Done)]) {
			return placementError("the host purge stages are not an ordered prefix")
		}
	}
	for leaf, intent := range control.CleanupPayloads {
		prefix := "." + brand.Name + "-cleanup-" + intent.Nonce + "."
		if intent.Owner.extra != "" || len(intent.extra) != 0 || intent.Scope != control.Scope || intent.Nonce != control.Nonce || leaf != intent.Leaf || !strings.HasPrefix(leaf, prefix) || len(strings.TrimPrefix(leaf, prefix)) != 10 || strings.Trim(strings.TrimPrefix(leaf, prefix), "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789") != "" || !intent.Directory.Known() || !intent.Directory.Directory || intent.Directory.Mode != 0o700 || !intent.File.Known() || intent.File.Directory || intent.File.Mode != 0o700 || intent.Owner.Kind != "cleanup" || intent.Owner.PID <= 0 || intent.Owner.Birth == "" || intent.Owner.At.IsZero() {
			return placementError("the cleanup payload has an unknown admission receipt")
		}
	}
	for name, intent := range control.StateWrites {
		if name != "walls.json" && name != "idle.json" || !metadataWriteIntentKnown(intent) {
			return placementError("the state metadata publication intent is unknown")
		}
	}
	for name, intent := range control.ArchiveWrites {
		if name != "installs.json" && name != "footprint.json" || !metadataWriteIntentKnown(intent) {
			return placementError("the metadata archive intent is malformed")
		}
	}
	return nil
}
