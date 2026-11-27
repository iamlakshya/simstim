package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

func init() {
	if err := registerSavedHostRemovalParticipantV1("joined-ssh-v1", func(ctx context.Context, phase *savedHostRemovalPreparationV1) (savedHostRemovalParticipantV1, error) {
		participant, err := prepareJoinedSSHRemovalV1(ctx, phase)
		if participant == nil {
			return nil, err
		}
		return participant, err
	}); err != nil {
		panic(err)
	}
}

type joinedRemovalJournal struct {
	Version        int    `json:"version"`
	Commit         string `json:"commit"`
	OriginalSHA256 string `json:"original_sha256"`
	HostID         string `json:"host_id"`
	Generation     string `json:"generation"`
	Action         string `json:"action"`
	Leaf           string `json:"leaf,omitempty"`
}

func retiredJoinedLeaf(leaf string) string { return ".retired-" + leaf }
func (r *joinedSSHRemovalV1) phaseHost(h Host, id string) error {
	if r == nil || r.closed || !SameEntry(r.original, h) || id != r.hostID {
		return hostStateRemovalError("the joined consumer belongs to another original removal")
	}
	return nil
}

type joinedRemovalEffectPhaseV1 interface {
	CheckParticipant(context.Context, *savedHostRemovalPreparationV1) error
}

func (r *joinedSSHRemovalV1) writeJournal(ctx context.Context, phase joinedRemovalEffectPhaseV1, action, leaf string) error {
	if err := phase.CheckParticipant(ctx, r.preparation); err != nil {
		return err
	}
	if r.journal == nil {
		f, err := r.root.OpenFile("cleanup.json", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if err != nil {
			return err
		}
		r.journal = f
		r.journalInfo, err = f.Stat()
		if err != nil {
			return err
		}
	}
	info, err := cachefs.ValidateFile(r.root, "cleanup.json", r.journal, 0o600)
	if err != nil || !os.SameFile(info, r.journalInfo) {
		return hostStateRemovalError("the original joined cleanup journal changed")
	}
	if joinedOriginalSHA256V1(r.original) == "" {
		return hostStateRemovalError("the original joined saved entry cannot be encoded")
	}
	body, err := json.Marshal(joinedRemovalJournal{1, r.record.Commit, joinedOriginalSHA256V1(r.original), r.hostID, r.generation, action, leaf})
	if err != nil {
		return err
	}
	body = append(body, '\n')
	if err = phase.CheckParticipant(ctx, r.preparation); err != nil {
		return err
	}
	if err = r.journal.Truncate(0); err != nil {
		return err
	}
	if err = phase.CheckParticipant(ctx, r.preparation); err != nil {
		return err
	}
	if _, err = r.journal.WriteAt(body, 0); err != nil {
		return err
	}
	if err = phase.CheckParticipant(ctx, r.preparation); err != nil {
		return err
	}
	return r.journal.Sync()
}
func (r *joinedSSHRemovalV1) stage(ctx context.Context, phase *savedHostRemovalStageV1) error {
	if phase == nil {
		return hostStateRemovalError("the joined staging token is missing")
	}
	if err := phase.CheckParticipant(ctx, r.preparation); err != nil {
		return err
	}
	if err := r.phaseHost(phase.Host(), phase.HostID()); err != nil {
		return err
	}
	if r.finalising {
		return hostStateRemovalError("joined finalisation already began")
	}
	r.generation = phase.TargetGeneration()
	if err := r.checkRetained(); err != nil {
		return err
	}
	for n, pair := range r.manifest.Files {
		if r.moved[n] {
			continue
		}
		if err := phase.CheckParticipant(ctx, r.preparation); err != nil {
			return err
		}
		if err := r.manifest.Directory.CheckRoot(r.root); err != nil {
			return err
		}
		if err := checkJoinedPair(r.root, pair, r.files[n], 2); err != nil {
			return err
		}
		if err := r.writeJournal(ctx, phase, "rename-pending", pair.Leaf); err != nil {
			return err
		}
		if err := phase.CheckParticipant(ctx, r.preparation); err != nil {
			return err
		}
		if err := cachefs.RenameNoReplace(r.root, pair.Leaf, retiredJoinedLeaf(pair.Leaf)); err != nil {
			return err
		}
		r.moved[n] = true
		if err := checkJoinedPairNames(r.root, pair, r.files[n], retiredJoinedLeaf(pair.Leaf), joinedAnchor(pair.Leaf), 2); err != nil {
			return err
		}
		if err := r.writeJournal(ctx, phase, "renamed", pair.Leaf); err != nil {
			return err
		}
	}
	return nil
}
func (r *joinedSSHRemovalV1) rollback(ctx context.Context, phase *savedHostRemovalRollbackV1) error {
	if phase == nil {
		return hostStateRemovalError("the joined rollback token is missing")
	}
	if err := phase.CheckParticipant(ctx, r.preparation); err != nil {
		return err
	}
	if err := r.phaseHost(phase.Host(), phase.HostID()); err != nil {
		return err
	}
	if r.finalising {
		return hostStateRemovalError("committed joined cleanup cannot roll back")
	}
	for n := len(r.manifest.Files) - 1; n >= 0; n-- {
		if !r.moved[n] {
			continue
		}
		pair := r.manifest.Files[n]
		if err := phase.CheckParticipant(ctx, r.preparation); err != nil {
			return err
		}
		if err := checkJoinedPairNames(r.root, pair, r.files[n], retiredJoinedLeaf(pair.Leaf), joinedAnchor(pair.Leaf), 2); err != nil {
			return err
		}
		if err := r.writeJournal(ctx, phase, "restore-pending", pair.Leaf); err != nil {
			return err
		}
		if err := phase.CheckParticipant(ctx, r.preparation); err != nil {
			return err
		}
		if err := cachefs.RenameNoReplace(r.root, retiredJoinedLeaf(pair.Leaf), pair.Leaf); err != nil {
			return err
		}
		r.moved[n] = false
		if err := checkJoinedPair(r.root, pair, r.files[n], 2); err != nil {
			return err
		}
		if err := r.writeJournal(ctx, phase, "restored", pair.Leaf); err != nil {
			return err
		}
	}
	if r.journal != nil {
		if err := phase.CheckParticipant(ctx, r.preparation); err != nil {
			return err
		}
		info, err := cachefs.ValidateFile(r.root, "cleanup.json", r.journal, 0o600)
		if err != nil || !os.SameFile(info, r.journalInfo) {
			return hostStateRemovalError("the original joined rollback journal changed")
		}
		if err = phase.CheckParticipant(ctx, r.preparation); err != nil {
			return err
		}
		if err = r.root.Remove("cleanup.json"); err != nil {
			return err
		}
		if err = r.journal.Close(); err != nil {
			return err
		}
		r.journal = nil
	}
	return nil
}
func (r *joinedSSHRemovalV1) finalise(ctx context.Context, phase *savedHostRemovalCommitV1) error {
	if phase == nil {
		return hostStateRemovalError("the actual joined commit token is missing")
	}
	if err := phase.CheckParticipant(ctx, r.preparation); err != nil {
		return err
	}
	if err := r.phaseHost(phase.Host(), phase.HostID()); err != nil {
		return err
	}
	if r.finalised {
		return nil
	}
	if r.generation != "" && r.generation != phase.TargetGeneration() {
		return hostStateRemovalError("the joined forget target changed")
	}
	r.generation = phase.TargetGeneration()
	r.finalising = true
	for n, pair := range r.manifest.Files {
		if !r.moved[n] {
			return hostStateRemovalError("a joined operational leaf was not staged by its original consumer")
		}
		if r.anchorRemoved[n] {
			continue
		}
		if err := phase.CheckParticipant(ctx, r.preparation); err != nil {
			return err
		}
		if err := r.manifest.Directory.CheckRoot(r.root); err != nil {
			return err
		}
		if !r.leafRemoved[n] {
			if err := checkJoinedPairNames(r.root, pair, r.files[n], retiredJoinedLeaf(pair.Leaf), joinedAnchor(pair.Leaf), 2); err != nil {
				return err
			}
			if err := r.writeJournal(ctx, phase, "unlink-pending", pair.Leaf); err != nil {
				return err
			}
			if err := phase.CheckParticipant(ctx, r.preparation); err != nil {
				return err
			}
			if err := r.root.Remove(retiredJoinedLeaf(pair.Leaf)); err != nil {
				return err
			}
			r.leafRemoved[n] = true
		}
		if err := checkJoinedAnchorOnly(r.root, pair, r.files[n]); err != nil {
			return err
		}
		if err := r.writeJournal(ctx, phase, "anchor-unlink-pending", pair.Leaf); err != nil {
			return err
		}
		if err := phase.CheckParticipant(ctx, r.preparation); err != nil {
			return err
		}
		if err := r.root.Remove(joinedAnchor(pair.Leaf)); err != nil {
			return err
		}
		r.anchorRemoved[n] = true
	}
	manifestPair := joinedOwnedFile{"ownership.json", r.record.ManifestNative, r.record.ManifestSHA256}
	if !r.manifestLeafRemoved {
		if err := phase.CheckParticipant(ctx, r.preparation); err != nil {
			return err
		}
		if err := checkJoinedPair(r.root, manifestPair, r.manifestFile, 2); err != nil {
			return err
		}
		if err := r.writeJournal(ctx, phase, "manifest-unlink-pending", ""); err != nil {
			return err
		}
		if err := phase.CheckParticipant(ctx, r.preparation); err != nil {
			return err
		}
		if err := r.root.Remove("ownership.json"); err != nil {
			return err
		}
		r.manifestLeafRemoved = true
	}
	if !r.manifestAnchorRemoved {
		if err := phase.CheckParticipant(ctx, r.preparation); err != nil {
			return err
		}
		if err := checkJoinedAnchorOnly(r.root, manifestPair, r.manifestFile); err != nil {
			return err
		}
		if err := phase.CheckParticipant(ctx, r.preparation); err != nil {
			return err
		}
		if err := r.root.Remove("ownership.origin"); err != nil {
			return err
		}
		r.manifestAnchorRemoved = true
	}
	if !r.journalRemoved {
		if err := phase.CheckParticipant(ctx, r.preparation); err != nil {
			return err
		}
		if r.journal == nil {
			return hostStateRemovalError("the actual joined unlink journal owner is missing")
		}
		info, err := cachefs.ValidateFile(r.root, "cleanup.json", r.journal, 0o600)
		if err != nil || !os.SameFile(info, r.journalInfo) {
			return hostStateRemovalError("the actual joined unlink journal changed")
		}
		if err = phase.CheckParticipant(ctx, r.preparation); err != nil {
			return err
		}
		if err = r.root.Remove("cleanup.json"); err != nil {
			return err
		}
		r.journalRemoved = true
	}
	if !r.directoryRemoved {
		if err := phase.CheckParticipant(ctx, r.preparation); err != nil {
			return err
		}
		if err := r.manifest.Directory.CheckRoot(r.root); err != nil {
			return err
		}
		parent, err := cachefs.OpenPrivateRoot(filepath.Dir(r.directory), false)
		if err != nil {
			return err
		}
		defer parent.Close()
		current, err := parent.Lstat(filepath.Base(r.directory))
		if err != nil {
			return err
		}
		here, err := r.root.Stat(".")
		if err != nil || !os.SameFile(here, current) {
			return hostStateRemovalError("the original joined directory changed at final unlink")
		}
		if err = phase.CheckParticipant(ctx, r.preparation); err != nil {
			return err
		}
		if err = parent.Remove(filepath.Base(r.directory)); err != nil {
			return err
		}
		r.directoryRemoved = true
	}
	r.finalised = true
	return nil
}
func checkJoinedAnchorOnly(root *os.Root, pair joinedOwnedFile, file *os.File) error {
	native, err := joinedNativeIdentity(file, 1)
	if err != nil || native != pair.Native {
		return hostStateRemovalError("the original joined anchor has changed shape")
	}
	current, err := root.Lstat(joinedAnchor(pair.Leaf))
	if err != nil {
		return err
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(current, opened) {
		return hostStateRemovalError("the original joined anchor was replaced")
	}
	b, err := io.ReadAll(io.NewSectionReader(file, 0, 1<<20))
	if err != nil {
		return err
	}
	if joinedDigest(b) != pair.SHA256 {
		return hostStateRemovalError("the original joined anchor's bytes changed")
	}
	return nil
}
func checkJoinedPairNames(root *os.Root, pair joinedOwnedFile, file *os.File, leaf, anchor string, links uint64) error {
	native, err := joinedNativeIdentity(file, links)
	if err != nil || native != pair.Native {
		return hostStateRemovalError("the original staged joined inode changed")
	}
	opened, err := file.Stat()
	if err != nil {
		return err
	}
	for _, name := range []string{leaf, anchor} {
		info, err := root.Lstat(name)
		if err != nil || !info.Mode().IsRegular() || !os.SameFile(info, opened) {
			return hostStateRemovalError("an original joined staged leaf or anchor changed")
		}
	}
	body, err := io.ReadAll(io.NewSectionReader(file, 0, 1<<20))
	if err != nil {
		return err
	}
	if joinedDigest(body) != pair.SHA256 {
		return hostStateRemovalError("the original joined staged bytes changed")
	}
	return nil
}
func (r *joinedSSHRemovalV1) close() error {
	if r == nil || r.closed {
		return nil
	}
	if r.finalising && !r.finalised {
		return hostStateRemovalError("the original joined finalisation is still owned and incomplete")
	}
	if r.ready && !r.finalised {
		for _, moved := range r.moved {
			if moved {
				return hostStateRemovalError("the original joined rollback is still owned and incomplete")
			}
		}
	}
	var failure error
	for _, file := range r.files {
		failure = errors.Join(failure, file.Close())
	}
	if r.manifestFile != nil {
		failure = errors.Join(failure, r.manifestFile.Close())
	}
	if r.journal != nil {
		failure = errors.Join(failure, r.journal.Close())
	}
	if r.root != nil {
		failure = errors.Join(failure, r.root.Close())
	}
	if failure == nil {
		r.closed = true
	}
	return failure
}

// recoverJoinedPreparation accepts a previous staged name only inside a NEW
// actual current saved-Source preparation. It never runs after a cold forget.
func (r *joinedSSHRemovalV1) recoverJoinedPreparation() error {
	file, err := cachefs.OpenFile(r.root, "cleanup.json", os.O_RDWR, 0o600)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	r.journal = file
	r.journalInfo, err = file.Stat()
	if err != nil {
		return err
	}
	body, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil || len(body) > 1<<20 {
		return hostStateRemovalError("the joined recovery journal is too large or unreadable")
	}
	var journal joinedRemovalJournal
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&journal); err != nil {
		return err
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || journal.Version != 1 || journal.Commit != r.record.Commit || journal.OriginalSHA256 != joinedOriginalSHA256V1(r.original) || journal.HostID != r.hostID {
		return hostStateRemovalError("the joined recovery journal belongs to another original Source")
	}
	switch journal.Action {
	case "rename-pending", "renamed", "restore-pending", "restored":
	default:
		return hostStateRemovalError("cold post-forget joined cleanup cannot adopt a former live owner")
	}
	return nil
}

func joinedOriginalSHA256V1(host Host) string {
	body, err := json.Marshal(entryOf(host))
	if err != nil {
		return ""
	}
	return joinedDigest(body)
}
