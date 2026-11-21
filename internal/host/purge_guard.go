package host

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/supply"
)

type purgeGuardFileV1 struct {
	Leaf   string   `json:"leaf"`
	Ref    purgeRef `json:"ref"`
	SHA256 string   `json:"sha256"`
}

type purgeMarkerV1 struct {
	Version int                `json:"v"`
	HostID  string             `json:"host_id"`
	Nonce   string             `json:"operation_nonce"`
	Scope   string             `json:"scope"`
	Token   string             `json:"token"`
	Files   []purgeGuardFileV1 `json:"files"`
}

func (owner *purgeOwnerV1) prepareFinalGuard() error {
	if owner.report.GuardWitness != nil && owner.report.GuardWitness.Prepared() {
		ready, err := actualPurgeFinalMarkerReadyV2(owner)
		if err != nil {
			return err
		}
		if ready {
			return nil
		}
	}
	if !owner.phaseDone("host-complete") {
		if err := owner.completePhase("host-complete"); err != nil {
			return err
		}
	}
	// This final read transaction writes its own stable control once, before
	// sealing marker contents. The ordinary gate must not rewrite that inode.
	return owner.placement.withControlRoot(owner.ctx, func(control *placementControl, guard *os.Root) error {
		if control.HostID != owner.args.HostID || control.Nonce != owner.args.Nonce || control.OwnerRun != owner.run || control.Owner == nil || *control.Owner != owner.process {
			return placementError("the final purge lost its exact original owner")
		}
		if control.Pending != nil || len(control.Active) != 0 || len(control.CleanupPayloads) != 0 {
			return placementError("the final purge still has an original resource or publisher to settle")
		}
		marker, leaf, token, retained, err := reuseCreatedPurgeFinalMarkerV2(owner, guard)
		if err != nil {
			return err
		}
		if marker == nil {
			token, err = footprintNonce()
			if err != nil {
				return err
			}
			leaf = ".final-marker-" + token
			marker, err = cachefs.OpenFile(guard, leaf, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return err
			}
			retained, err = retainCreatedPurgeFinalMarkerV2(owner, guard, marker, leaf)
			if !retained {
				defer marker.Close()
			}
			if err != nil {
				return err
			}
		}
		info, err := marker.Stat()
		if err != nil {
			return err
		}
		original := api.PurgeGuardWitness{Version: 1, HostID: control.HostID, Nonce: control.Nonce, Scope: control.Scope, User: owner.report.User, Arch: owner.report.Arch, Token: token, Root: apiPurgeRef(control.Control), Sentinel: apiPurgeRef(control.ControlLock), Marker: apiPurgeRef(refOf(info))}
		if !original.Known() {
			return placementError("the final marker has no original native creation witness")
		}
		control.GuardWitness = &original
		control.Phase = "finalising"
		owner.report.HostComplete = true
		savedReport := owner.report
		savedReport.GuardWitness = nil
		control.Report = &savedReport
		if err := writePlacementControl(guard, *control); err != nil {
			return err
		}
		plan := purgeMarkerV1{Version: 1, HostID: original.HostID, Nonce: original.Nonce, Scope: original.Scope, Token: original.Token, Files: []purgeGuardFileV1{}}
		for _, name := range []string{"installs.json", "footprint.json", "control.json"} {
			before, err := metadataBefore(guard, name)
			if err != nil {
				return err
			}
			if before == nil {
				continue
			}
			if name != "control.json" && !control.Archives[name].matches(before) {
				return placementError("the terminal archive changed its original producer receipt")
			}
			file, err := cachefs.OpenFile(guard, name, os.O_RDONLY, 0o600)
			if err != nil {
				return err
			}
			digest, err := purgeFileDigestV1(owner.ctx, file)
			checked, checkErr := cachefs.ValidateFile(guard, name, file, 0o600)
			_ = file.Close()
			if err != nil || checkErr != nil || !refOf(before).matches(checked) {
				return placementError("the terminal archive changed while its witness was sealed")
			}
			plan.Files = append(plan.Files, purgeGuardFileV1{Leaf: name, Ref: refOf(before), SHA256: digest})
		}
		body, err := json.Marshal(plan)
		if err != nil {
			return err
		}
		if _, err := marker.Write(body); err != nil {
			return err
		}
		if err := marker.Sync(); err != nil {
			return err
		}
		if err := syncCapturedDirectory(guard); err != nil {
			return err
		}
		info, err = cachefs.ValidateFile(guard, leaf, marker, 0o600)
		if err != nil || !nativePurgeRef(original.Marker).samePreparedIdentity(info) {
			return placementError("the prepared final marker changed its original inode")
		}
		prepared := original
		prepared.Marker = apiPurgeRef(refOf(info))
		prepared.MarkerSHA256 = footprintDigest(body)
		if !prepared.Prepared() {
			return placementError("the final marker has no complete content witness")
		}
		owner.report.GuardWitness = &prepared
		return sealCreatedPurgeFinalMarkerV2(owner, body)
	}, false)
}

func readPurgeMarkerV1(ctx context.Context, guard *os.Root, w api.PurgeGuardWitness) (purgeMarkerV1, error) {
	var plan purgeMarkerV1
	leaf := ".final-marker-" + w.Token
	file, err := cachefs.OpenFile(guard, leaf, os.O_RDONLY, 0o600)
	if err != nil {
		return plan, err
	}
	defer file.Close()
	info, err := cachefs.ValidateFile(guard, leaf, file, 0o600)
	if err != nil || !nativePurgeRef(w.Marker).matches(info) {
		return plan, placementError("the final marker changed its native generation")
	}
	raw, err := io.ReadAll(io.LimitReader(file, 16<<10+1))
	if err != nil || len(raw) > 16<<10 || footprintDigest(raw) != w.MarkerSHA256 || !api.ExactJSONFields(raw, plan) || json.Unmarshal(raw, &plan) != nil || plan.Version != 1 || plan.HostID != w.HostID || plan.Nonce != w.Nonce || plan.Scope != w.Scope || plan.Token != w.Token {
		return plan, placementError("the final marker changed its exact original body")
	}
	seen := map[string]bool{}
	for _, entry := range plan.Files {
		if (entry.Leaf != "control.json" && entry.Leaf != "footprint.json" && entry.Leaf != "installs.json") || seen[entry.Leaf] || !supply.IsHex64(entry.SHA256) || entry.Ref.Mode != 0o600 || len(entry.Ref.extra) != 0 || entry.Ref.Inode == 0 || entry.Ref.UID != uint64(os.Getuid()) {
			return plan, placementError("the sealed terminal plan has an unknown fixed role")
		}
		seen[entry.Leaf] = true
	}
	if !seen["control.json"] {
		return plan, placementError("the terminal plan omitted its original control")
	}
	return plan, ctx.Err()
}

// CommitPurgeGuardContext accepts only the exact saved native acknowledgement.
// Its fixed roles survive partial terminal cleanup; no helper is installed.
func CommitPurgeGuardContext(ctx context.Context, w api.PurgeGuardWitness) error {
	if ctx == nil || !w.Prepared() {
		return placementError("the final acknowledgement has no complete saved witness")
	}
	if err := errors.Join(ctx.Err(), CheckCaller()); err != nil {
		return err
	}
	scope, path, err := placementScope()
	if err != nil || scope != w.Scope || nativePurgeRef(w.Root).UID != uint64(os.Getuid()) {
		return placementError("the acknowledgement belongs to another original home scope")
	}
	return withPlacementHome(ctx, func(home *os.Root) error {
		guard, err := cachefs.OpenPrivateRoot(path, false)
		if errors.Is(err, os.ErrNotExist) {
			path = filepath.Join(filepath.Dir(path), finalGuardLeaf(scope))
			guard, err = cachefs.OpenPrivateRoot(path, false)
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
		}
		if err != nil {
			return err
		}
		defer guard.Close()
		if err := nativePurgeRef(w.Root).matchesRoot(guard); err != nil {
			return err
		}
		markerLeaf := ".final-marker-" + w.Token
		markerCapture := ".terminal-marker-" + w.Token
		// The last marker capture can be replayed from its exact saved reference.
		if _, err := guard.Lstat(markerLeaf); errors.Is(err, os.ErrNotExist) {
			if _, err := guard.Lstat(markerCapture); err == nil {
				if err := removePurgeTerminalFileV1(ctx, guard, purgeGuardFileV1{Leaf: markerCapture, Ref: nativePurgeRef(w.Marker), SHA256: w.MarkerSHA256}, ".terminal-marker-final-"+w.Token); err != nil {
					return err
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			return removeEmptyPurgeGuardV1(ctx, home, guard, path, w)
		}
		lock, lockErr := cachefs.OpenFile(guard, "control.lock", os.O_RDWR, 0o600)
		if lockErr == nil {
			defer lock.Close()
			info, err := cachefs.ValidateFile(guard, "control.lock", lock, 0o600)
			if err != nil || !nativePurgeRef(w.Sentinel).matches(info) {
				return placementError("the final sentinel changed its original native generation")
			}
			unlock, err := waitPlacementFile(ctx, guard, "control.lock", lock, info)
			if err != nil {
				return err
			}
			defer unlock()
		} else if !errors.Is(lockErr, os.ErrNotExist) {
			return lockErr
		}
		plan, err := readPurgeMarkerV1(ctx, guard, w)
		if err != nil {
			return err
		}
		allowed := map[string]bool{markerLeaf: true, "control.lock": true, ".terminal-lock-" + w.Token: true}
		for _, entry := range plan.Files {
			allowed[entry.Leaf] = true
			allowed[".terminal-"+w.Token+"-"+entry.Leaf] = true
		}
		file, err := guard.Open(".")
		if err != nil {
			return err
		}
		entries, err := file.ReadDir(-1)
		_ = file.Close()
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if !allowed[entry.Name()] {
				return placementError("the final guard contains an unknown preserved resource")
			}
		}
		// Archives retain their exact native identity and sealed contents even if
		// a previous acknowledgement died after moving a public name aside.
		for _, entry := range plan.Files {
			if err := removePurgeTerminalFileV1(ctx, guard, entry, ".terminal-"+w.Token+"-"+entry.Leaf); err != nil {
				return err
			}
		}
		if err := removePurgeTerminalFileV1(ctx, guard, purgeGuardFileV1{Leaf: "control.lock", Ref: nativePurgeRef(w.Sentinel), SHA256: footprintDigest(nil)}, ".terminal-lock-"+w.Token); err != nil {
			return err
		}
		if err := removePurgeTerminalFileV1(ctx, guard, purgeGuardFileV1{Leaf: markerLeaf, Ref: nativePurgeRef(w.Marker), SHA256: w.MarkerSHA256}, markerCapture); err != nil {
			return err
		}
		return removeEmptyPurgeGuardV1(ctx, home, guard, path, w)
	})
}

func removePurgeTerminalFileV1(ctx context.Context, root *os.Root, entry purgeGuardFileV1, capture string) error {
	leaf := entry.Leaf
	if _, err := root.Lstat(leaf); errors.Is(err, os.ErrNotExist) {
		leaf = capture
	} else if err != nil {
		return err
	}
	if _, err := root.Lstat(leaf); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	file, err := cachefs.OpenFile(root, leaf, os.O_RDONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := cachefs.ValidateFile(root, leaf, file, 0o600)
	digest, hashErr := purgeFileDigestV1(ctx, file)
	if err != nil || hashErr != nil || !entry.Ref.matches(info) || digest != entry.SHA256 {
		return placementError("preserved a changed terminal native file")
	}
	if leaf != capture {
		if err := cachefs.RenameNoReplace(root, leaf, capture); err != nil {
			return err
		}
	}
	info, err = cachefs.ValidateFile(root, capture, file, 0o600)
	digest, hashErr = purgeFileDigestV1(ctx, file)
	if err != nil || hashErr != nil || !entry.Ref.matches(info) || digest != entry.SHA256 {
		return placementError("preserved a changed captured terminal file")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := root.Remove(capture); err != nil {
		return err
	}
	return syncCapturedDirectory(root)
}

func removeEmptyPurgeGuardV1(ctx context.Context, home, guard *os.Root, path string, w api.PurgeGuardWitness) error {
	if err := errors.Join(ctx.Err(), nativePurgeRef(w.Root).matchesRoot(guard)); err != nil {
		return err
	}
	directory, err := guard.Open(".")
	if err != nil {
		return err
	}
	entries, err := directory.ReadDir(1)
	_ = directory.Close()
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if len(entries) != 0 {
		return placementError("the terminal guard still contains preserved files")
	}
	leaf := filepath.Base(path)
	capture := finalGuardLeaf(w.Scope)
	if leaf != capture {
		if err := cachefs.RenameOwnedDirectoryNoReplace(home, leaf, capture); err != nil {
			return err
		}
	}
	current, err := home.Lstat(capture)
	if err != nil || !nativePurgeRef(w.Root).matches(current) {
		return placementError("preserved a replaced terminal guard capture")
	}
	if err := home.Remove(capture); err != nil {
		return err
	}
	return syncCapturedDirectory(home)
}
