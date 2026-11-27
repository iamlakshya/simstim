package client

import (
	"context"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

func prepareHostTerminalRemoval(ctx context.Context, original Host, validators map[string]func([]byte) error) (result HostStateRemovalResult, failure error) {
	if err := stateContextAuthority(ctx); err != nil {
		return result, err
	}
	intent, _ := ctx.Value(hostForgetIntentKey{}).(*hostForgetIntent)
	scope, _ := ctx.Value(hostForgetMembershipKey{}).(*hostForgetMembership)
	held, _ := ctx.Value(heldKey{original.Name}).(*heldLock)
	if intent == nil || intent.hooks.Inspect == nil || scope == nil || held == nil {
		return result, hostStateRemovalError("terminal cleanup requires the canonical complete receipt inspector")
	}
	defer func() {
		if failure != nil {
			reportHostTerminalFailure(ctx, original.Name, &result)
			result.Retained = append(result.Retained, hostLockPath(original.Name))
			result.Removed = compactHostStatePaths(result.Removed)
			result.Retained = compactHostStatePaths(result.Retained)
			result.Preserved = compactHostStatePaths(result.Preserved)
		}
	}()
	if len(validators) != len(hostStateFiles) {
		return result, hostStateRemovalError("the known state owner validators are incomplete")
	}
	for _, name := range hostStateFiles {
		if validators[name] == nil {
			return result, hostStateRemovalError("a known state file has no owner validator")
		}
	}
	validators = maps.Clone(validators)
	info, err := held.native.file.Stat()
	if err != nil {
		return result, err
	}
	prior, frameErr := readHostTerminalFrame(ctx, held.native.file, info.Size())
	complete := frameErr == nil
	if frameErr != nil && !errors.Is(frameErr, errHostTerminalIncomplete) {
		return result, frameErr
	}
	if info.Size() != 0 && len(prior.prefix) == 0 {
		return result, hostStateRemovalError("the torn checkpoint has no complete native inventory to reconstruct")
	}
	failure = withHostStateSet(ctx, []string{"hosts", HostFileKey(original.Name)}, 0, nil, func(all []*stateFileTxn) error {
		current, err := captureHostTerminalInventory(ctx, original, held, all, &result)
		if err != nil {
			return err
		}
		var invalid error
		for _, tx := range all {
			if tx.source == nil {
				continue
			}
			if err := validateHostStateOwner(ctx, tx, validators[tx.name]); err != nil {
				path := filepath.Join(removalDir(original.Name), tx.name)
				var retained *retainedHostStateError
				if errors.As(err, &retained) {
					result.Retained = append(result.Retained, path)
				} else {
					result.Preserved = append(result.Preserved, path)
				}
				invalid = errors.Join(invalid, err)
			}
		}
		if invalid != nil {
			return invalid
		}
		var identity HostStateReceiptIdentity
		var source stateFileView
		if complete {
			view := &stateFDView{ctx: ctx, r: io.NewSectionReader(held.native.file, prior.receiptStart, prior.header.ReceiptSize)}
			defer view.revoke()
			identity, err = inspectHostTerminalOwnerView(ctx, intent, stateFileView{ReaderAt: view, Size: prior.header.ReceiptSize, Exists: true})
			if err != nil {
				return err
			}
			if !terminalIdentityMatches(prior.header, identity) {
				return hostStateRemovalError("the complete checkpoint changed owner identity")
			}
			if all[3].source != nil {
				source = all[3].view()
				if source.Size != prior.header.ReceiptSize {
					return hostStateRemovalError("the remaining source receipt differs from the immutable checkpoint")
				}
				if err := compareHostTerminalPrefix(ctx, source.ReaderAt, view, source.Size); err != nil {
					return err
				}
			}
		} else {
			source = all[3].view()
			if !source.Exists {
				return hostStateRemovalError("terminal reconstruction requires its exact still-present source receipt")
			}
			identity, err = inspectHostTerminalOwnerView(ctx, intent, source)
			if err != nil {
				return err
			}
		}
		if err := validateHostTerminalIdentity(identity, original.Name); err != nil {
			return err
		}
		if !SameEntry(identity.Original, original) || identity.HostID != intent.hostID || identity.Proof.TargetGeneration != scope.generation {
			return hostStateRemovalError("the checkpoint receipt differs from the actual verified forget")
		}
		current.HostID, current.Nonce = identity.HostID, identity.Nonce
		current.From, current.Target = identity.Proof.FromGeneration, identity.Proof.TargetGeneration
		current.ReceiptSize = source.Size
		var frame hostTerminalFrame
		if info.Size() == 0 {
			frame, err = encodeHostTerminalFrame(current)
		} else {
			frame = prior
			if !terminalIdentityMatches(frame.header, identity) {
				return hostStateRemovalError("the reconstructed checkpoint has another original attempt")
			}
			if err := checkHostTerminalInventory(frame.header, current, complete); err != nil {
				return err
			}
		}
		if err != nil {
			return err
		}
		if !complete {
			if err := writeHostTerminalCheckpoint(ctx, held, frame, source); err != nil {
				return err
			}
		} else {
			if err := syncExistingHostTerminalCheckpoint(ctx, held, frame); err != nil {
				return err
			}
			// Reread the complete raw owner receipt after Sync as well as its
			// frame. No first-call durability success is inferred from a footer.
			view := &stateFDView{ctx: ctx, r: io.NewSectionReader(held.native.file, frame.receiptStart, frame.header.ReceiptSize)}
			defer view.revoke()
			reread, err := inspectHostTerminalOwnerView(ctx, intent, stateFileView{ReaderAt: view, Size: frame.header.ReceiptSize, Exists: true})
			if err != nil {
				return err
			}
			if !SameEntry(reread.Original, identity.Original) || !terminalIdentityMatches(frame.header, reread) {
				return hostStateRemovalError("the durable terminal receipt changed original ownership")
			}
			if source.Exists {
				if err := compareHostTerminalPrefix(ctx, source.ReaderAt, view, source.Size); err != nil {
					return err
				}
			}
		}
		// Native views remain live until this callback unwinds. Only the outer
		// scope may consume this plan after every nested view has been revoked.
		result.Retained = terminalRemainingPaths(frame.header, current)
		result.Notes = []string{"Local retirement is pending the outer verified cleanup epilogue."}
		outcome := &hostTerminalOutcome{result: cloneHostStateRemovalResult(result)}
		checkpoint, err := cachefs.CaptureFile(held.native.root, held.native.name, held.native.file, 0600)
		if err != nil {
			return err
		}
		plan := &hostTerminalPlan{frame: frame, op: held, outcome: outcome, checkpoint: checkpoint}
		if err := scope.queueTerminal(plan); err != nil {
			return err
		}
		result.outcome = outcome
		return nil
	})
	return result, failure
}

func captureHostTerminalInventory(ctx context.Context, original Host, held *heldLock, all []*stateFileTxn, result *HostStateRemovalResult) (header hostTerminalHeader, failure error) {
	header.Name, header.Key = original.Name, HostFileKey(original.Name)
	if err := held.checkStateOperation(ctx); err != nil {
		return header, err
	}
	op, err := cachefs.CaptureFile(held.native.root, held.native.name, held.native.file, 0600)
	if err != nil {
		return header, err
	}
	parent, err := cachefs.CaptureRoot(held.native.root)
	if err != nil {
		return header, err
	}
	header.OP, header.Parent = terminalFacet(op), terminalFacet(parent)
	for i, tx := range all {
		if err := tx.check(); err != nil {
			return header, err
		}
		if tx.root == nil {
			if header.Folder != nil {
				return header, hostStateRemovalError("the captured state folder changed between canonical transactions")
			}
			continue
		}
		folder, err := cachefs.CaptureRoot(tx.root)
		if err != nil {
			return header, err
		}
		facet := terminalFacet(folder)
		if i > 0 && (header.Folder == nil || *header.Folder != facet) {
			return header, hostStateRemovalError("the four canonical transactions refer to different folders")
		}
		header.Folder = &facet
		if tx.source != nil {
			reference, err := cachefs.CaptureFile(tx.root, tx.name, tx.source, 0600)
			if err != nil {
				return header, err
			}
			reference.Modified = reference.Modified.UTC()
			header.Data[i] = &reference
		}
		if tx.lock != nil {
			reference, err := cachefs.CaptureFile(tx.root, tx.lock.name, tx.lock.file, 0600)
			if err != nil {
				return header, err
			}
			reference.Modified = reference.Modified.UTC()
			header.Locks[i] = &reference
		}
	}
	if all[0].root != nil {
		entries, err := hostStateLeftovers(ctx, all[0].root, removalDir(original.Name), all[0].anchors)
		if err != nil {
			return header, err
		}
		for _, path := range entries {
			if !knownHostStateEntry(filepath.Base(path)) {
				result.Preserved = append(result.Preserved, path)
				return header, hostStateRemovalError("unknown local host state was preserved before checkpointing")
			}
		}
	}
	file, err := cachefs.OpenFile(held.native.root, held.native.name+".who", os.O_RDONLY, 0600)
	if errors.Is(err, os.ErrNotExist) {
		return header, nil
	}
	if err != nil {
		return header, err
	}
	defer file.Close()
	reference, err := cachefs.CaptureFile(held.native.root, held.native.name+".who", file, 0600)
	if err != nil {
		return header, err
	}
	reference.Modified = reference.Modified.UTC()
	header.Who = &reference
	return header, ctx.Err()
}

func inspectHostTerminalOwnerView(ctx context.Context, intent *hostForgetIntent, view stateFileView) (HostStateReceiptIdentity, error) {
	identity, err := intent.hooks.Inspect(ctx, view.ReaderAt, view.Size, intent.original.Name)
	if err != nil {
		return identity, err
	}
	if intent.hooks.Verify == nil {
		return identity, hostStateRemovalError("the original terminal attempt has no bound receipt verifier")
	}
	raw, err := io.ReadAll(io.NewSectionReader(view.ReaderAt, 0, view.Size))
	if err != nil {
		return identity, err
	}
	proof, err := intent.hooks.Verify(ctx, raw)
	if err != nil {
		return identity, err
	}
	if proof != identity.Proof {
		return identity, hostStateRemovalError("the terminal inspector and original attempt verifier disagree")
	}
	return identity, ctx.Err()
}

func reportHostTerminalFailure(ctx context.Context, name string, result *HostStateRemovalResult) {
	root, anchors, err := openStateDirRootContext(ctx, []string{"hosts", HostFileKey(name)}, "host state", false)
	if err != nil {
		return
	}
	defer root.Close()
	result.Retained = append(result.Retained, removalDir(name))
	paths, _ := hostStateLeftovers(ctx, root, removalDir(name), anchors)
	for _, path := range paths {
		if !slices.Contains(result.Retained, path) {
			result.Preserved = append(result.Preserved, path)
		}
	}
}
