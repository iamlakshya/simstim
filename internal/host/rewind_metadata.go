package host

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"time"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/checkpoint"
	"github.com/iamlakshya/simstim/internal/proto"
)

type rewindMetadataPartV1 struct {
	root                                  *rewindMemberRootV1
	before                                *os.File
	beforeRef                             cachefs.Reference
	savedBefore                           *os.File
	savedBeforeName                       string
	savedBeforeRef                        cachefs.Reference
	after                                 *os.File
	afterName                             string
	afterRef                              cachefs.Reference
	conversation                          string
	published, rolledBack                 bool
	beforeClosed                          bool
	savedBeforeRemoved, savedBeforeClosed bool
	afterRemoved, afterClosed             bool
	closeErr                              error
}

type rewindMetadataTransactionV1 struct {
	preparation *bridgeRewindPreparationV1
	parts       []*rewindMetadataPartV1
	closed      bool
	settled     bool
	prepared    bool
}

func prepareRewindMetadataV1(ctx context.Context, p *bridgeRewindPreparationV1, generation string, selected checkpoint.Metadata) (result *rewindMetadataTransactionV1, returned error) {
	if p == nil || p.stopped == nil || p.operation == nil {
		return nil, rewindFailureV1("conversation restoration has no original stopped owner")
	}
	if err := p.stopped.Check(ctx, p.operation); err != nil {
		return nil, err
	}
	if err := checkRewindConversationIDsV1(p.members, selected); err != nil {
		return nil, err
	}
	txn := &rewindMetadataTransactionV1{preparation: p}
	complete := false
	defer func() {
		if !complete {
			cleanup := txn.closeV1()
			returned = errors.Join(returned, cleanup)
			if cleanup != nil {
				result = txn
			}
		}
	}()
	for _, member := range p.members {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := member.rootRef.CheckRoot(member.root); err != nil {
			return nil, err
		}
		part := &rewindMetadataPartV1{root: member, conversation: selected.AgentSessionIDs[member.id]}
		txn.parts = append(txn.parts, part)
		var err error
		part.before, err = cachefs.OpenFile(member.root, "manifest.json", os.O_RDONLY, 0600)
		if err != nil {
			return nil, err
		}
		part.beforeRef, err = cachefs.CaptureFile(member.root, "manifest.json", part.before, 0600)
		if err != nil {
			return nil, err
		}
		raw, err := io.ReadAll(io.LimitReader(part.before, proto.MaxFrame+1))
		if err != nil || len(raw) > proto.MaxFrame {
			return nil, errors.Join(rewindFailureV1("the original member manifest exceeds its bound"), err)
		}
		var current Manifest
		if err := json.Unmarshal(raw, &current); err != nil {
			return nil, err
		}
		if current.ID != member.id || current.path != "" || member.id != p.operation.Primary() && current.SandboxOf != p.operation.Primary() {
			return nil, snapshotChanged()
		}
		part.savedBeforeName = ".rewind-" + generation + "-before"
		part.savedBefore, part.savedBeforeRef, err = writeRewindManifestPartV1(member.root, part.savedBeforeName, raw)
		if err != nil {
			return nil, err
		}
		current.Agent.AgentSessionID = part.conversation
		current.Agent.IDSource = "rewind"
		current.Agent.IDHistory = append(current.Agent.IDHistory, IDEvent{ID: part.conversation, Source: "rewind", At: time.Now().UTC()})
		if len(current.Agent.IDHistory) > 100 {
			current.Agent.IDHistory = current.Agent.IDHistory[len(current.Agent.IDHistory)-100:]
		}
		if member.id == p.operation.Primary() && current.Guest != nil {
			current.Guest.Refs = nil
			for _, ref := range selected.Refs {
				current.Guest.Refs = append(current.Guest.Refs, proto.BundleRef{Name: ref.Name, OID: ref.SHA})
			}
		}
		encoded, err := current.MarshalJSON()
		if err != nil {
			return nil, err
		}
		part.afterName = ".rewind-" + generation + "-after"
		part.after, part.afterRef, err = writeRewindManifestPartV1(member.root, part.afterName, encoded)
		if err != nil {
			return nil, err
		}
		if err := part.beforeRef.CheckFile(member.root, "manifest.json", part.before, 0600); err != nil {
			return nil, err
		}
	}
	complete = true
	txn.prepared = true
	return txn, nil
}

func writeRewindManifestPartV1(root *os.Root, name string, raw []byte) (*os.File, cachefs.Reference, error) {
	if len(raw) > proto.MaxFrame {
		return nil, cachefs.Reference{}, rewindFailureV1("a rewind manifest part exceeds its bound")
	}
	file, err := cachefs.OpenFile(root, name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, cachefs.Reference{}, err
	}
	created, err := cachefs.CaptureFile(root, name, file, 0600)
	if err != nil {
		return file, cachefs.Reference{}, err
	}
	if _, err := file.Write(raw); err != nil {
		return file, created, err
	}
	if err := file.Sync(); err != nil {
		return file, created, err
	}
	ref, err := cachefs.CaptureFile(root, name, file, 0600)
	if err != nil {
		return file, created, err
	}
	if err := syncCapturedDirectory(root); err != nil {
		return file, ref, err
	}
	return file, ref, nil
}

func (txn *rewindMetadataTransactionV1) rolesV1() []rewindManifestRoleV1 {
	if txn == nil {
		return nil
	}
	roles := make([]rewindManifestRoleV1, 0, len(txn.parts))
	for _, part := range txn.parts {
		roles = append(roles, rewindManifestRoleV1{Session: part.root.id, Root: part.root.rootRef, Before: part.beforeRef, SavedBeforeName: part.savedBeforeName, SavedBefore: part.savedBeforeRef, AfterName: part.afterName, After: part.afterRef, Conversation: part.conversation})
	}
	return roles
}

func (txn *rewindMetadataTransactionV1) publishV1(ctx context.Context) error {
	if txn == nil || txn.closed || !txn.prepared || txn.preparation == nil || txn.preparation.stopped == nil || len(txn.parts) != len(txn.preparation.members) {
		return rewindFailureV1("the conversation transaction has no original stopped owner")
	}
	p := txn.preparation
	if err := p.stopped.Check(ctx, p.operation); err != nil {
		return err
	}
	for _, part := range txn.parts {
		if part.published {
			if part.rolledBack {
				return rewindFailureV1("rolled-back conversation metadata cannot be published again")
			}
			if err := part.checkPublishedV1(ctx); err != nil {
				return err
			}
			continue
		}
		if err := errors.Join(part.root.rootRef.CheckRoot(part.root.root), part.beforeRef.CheckFile(part.root.root, "manifest.json", part.before, 0600), part.afterRef.CheckFile(part.root.root, part.afterName, part.after, 0600)); err != nil {
			return err
		}
	}
	for _, part := range txn.parts {
		if part.published {
			continue
		}
		if err := errors.Join(ctx.Err(), p.stopped.Check(ctx, p.operation), part.root.rootRef.CheckRoot(part.root.root), part.beforeRef.CheckFile(part.root.root, "manifest.json", part.before, 0600), part.afterRef.CheckFile(part.root.root, part.afterName, part.after, 0600)); err != nil {
			return err
		}
		if err := part.root.root.Rename(part.afterName, "manifest.json"); err != nil {
			return err
		}
		part.afterName = "manifest.json"
		part.published = true
		if err := part.checkPublishedV1(ctx); err != nil {
			return err
		}
		if p.journal != nil {
			if err := p.journal.phaseV1(ctx, "metadata_committing"); err != nil {
				return err
			}
		}
	}
	return p.stopped.Check(ctx, p.operation)
}

func (txn *rewindMetadataTransactionV1) rollbackV1(ctx context.Context) error {
	if txn == nil || txn.closed {
		return nil
	}
	p := txn.preparation
	if p == nil || p.stopped == nil {
		return rewindFailureV1("the conversation rollback lost its original stopped owner")
	}
	if err := p.stopped.Check(ctx, p.operation); err != nil {
		return err
	}
	for _, part := range txn.parts {
		if !part.published {
			continue
		}
		if part.rolledBack {
			if err := part.checkRolledBackV1(ctx); err != nil {
				return err
			}
			continue
		}
		if err := errors.Join(part.root.rootRef.CheckRoot(part.root.root), part.afterRef.CheckFile(part.root.root, "manifest.json", part.after, 0600), part.savedBeforeRef.CheckFile(part.root.root, part.savedBeforeName, part.savedBefore, 0600)); err != nil {
			return err
		}
		if err := part.root.root.Rename(part.savedBeforeName, "manifest.json"); err != nil {
			return err
		}
		part.savedBeforeName = "manifest.json"
		part.rolledBack = true
		if err := part.checkRolledBackV1(ctx); err != nil {
			return err
		}
		if p.journal != nil {
			if err := p.journal.phaseV1(ctx, "metadata_rolling_back"); err != nil {
				return err
			}
		}
	}
	txn.settled = true
	return p.stopped.Check(ctx, p.operation)
}

// A successful rename is physical progress, not a directory durability
// receipt. Every retry rechecks and syncs the SAME canonical object before a
// partially published or rolled-back cohort can advance to its next phase.
func (part *rewindMetadataPartV1) checkPublishedV1(ctx context.Context) error {
	if part == nil || part.root == nil || !part.published || part.rolledBack {
		return rewindFailureV1("the original member publication is not current")
	}
	if err := errors.Join(ctx.Err(), part.root.rootRef.CheckRoot(part.root.root), part.afterRef.CheckFile(part.root.root, "manifest.json", part.after, 0600)); err != nil {
		return err
	}
	return errors.Join(syncCapturedDirectory(part.root.root), part.afterRef.CheckFile(part.root.root, "manifest.json", part.after, 0600), ctx.Err())
}

func (part *rewindMetadataPartV1) checkRolledBackV1(ctx context.Context) error {
	if part == nil || part.root == nil || !part.published || !part.rolledBack {
		return rewindFailureV1("the original member rollback is not current")
	}
	if err := errors.Join(ctx.Err(), part.root.rootRef.CheckRoot(part.root.root), part.savedBeforeRef.CheckFile(part.root.root, "manifest.json", part.savedBefore, 0600)); err != nil {
		return err
	}
	return errors.Join(syncCapturedDirectory(part.root.root), part.savedBeforeRef.CheckFile(part.root.root, "manifest.json", part.savedBefore, 0600), ctx.Err())
}

func (txn *rewindMetadataTransactionV1) markCommittedV1(ctx context.Context) error {
	if txn == nil || txn.closed || txn.preparation == nil || txn.preparation.disk == nil || txn.preparation.journal == nil {
		return rewindFailureV1("metadata commit lost the actual disk and journal owners")
	}
	p := txn.preparation
	if p.disk.Journal().Phase != checkpoint.RestoreExchangedV1 || p.journal.data.Phase != "metadata_committed" {
		return rewindFailureV1("metadata cannot settle before the actual disk and durable journal commit")
	}
	if err := p.stopped.Check(ctx, p.operation); err != nil {
		return err
	}
	for _, part := range txn.parts {
		if !part.published || part.rolledBack {
			return rewindFailureV1("the original member metadata is not completely committed")
		}
		if err := part.afterRef.CheckFile(part.root.root, "manifest.json", part.after, 0600); err != nil {
			return err
		}
	}
	txn.settled = true
	return nil
}

func (txn *rewindMetadataTransactionV1) closeV1() (failure error) {
	if txn == nil || txn.closed {
		return nil
	}
	if !txn.settled {
		for _, part := range txn.parts {
			if part.published && !part.rolledBack {
				return rewindFailureV1("the original metadata transaction must commit or roll back before its owners close")
			}
		}
	}
	for _, part := range txn.parts {
		if part.closeErr != nil {
			failure = errors.Join(failure, part.closeErr)
			continue
		}
		for _, owned := range []struct {
			file            *os.File
			ref             cachefs.Reference
			name            string
			removed, closed *bool
		}{{part.savedBefore, part.savedBeforeRef, part.savedBeforeName, &part.savedBeforeRemoved, &part.savedBeforeClosed}, {part.after, part.afterRef, part.afterName, &part.afterRemoved, &part.afterClosed}} {
			if owned.file == nil {
				continue
			}
			if *owned.closed {
				continue
			}
			if owned.name != "manifest.json" && !*owned.removed {
				if err := owned.ref.CheckCreatedFile(part.root.root, owned.name, owned.file, 0600); err != nil {
					failure = errors.Join(failure, err)
					continue
				}
				if err := part.root.root.Remove(owned.name); err != nil {
					failure = errors.Join(failure, err)
					continue
				}
				*owned.removed = true
			}
			if err := owned.file.Close(); err != nil {
				part.closeErr = err
				failure = errors.Join(failure, err)
			} else {
				*owned.closed = true
			}
		}
		if part.before != nil && !part.beforeClosed {
			if err := part.before.Close(); err != nil {
				part.closeErr = errors.Join(part.closeErr, err)
				failure = errors.Join(failure, err)
			} else {
				part.beforeClosed = true
			}
		}
		failure = errors.Join(failure, syncCapturedDirectory(part.root.root))
	}
	if failure == nil {
		txn.closed = true
	}
	return failure
}
