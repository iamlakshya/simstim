package client

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
)

var hostStateFiles = [...]string{"baseline.json", "root-steps.json", "sandboxes.json", "removal.json"}
var beforeHostStateUnlink = func(string) {}

// HostStateRemovalResult distinguishes committed removal from the deliberately
// retained domains and from refused or unknown state. It is not a clean home.
type HostStateRemovalResult struct {
	outcome *hostTerminalOutcome
	// Removed names files whose unlink actually committed, even on later error.
	Removed []string
	// Retained names validated domains and the receipt needed for later retries.
	Retained []string
	// Preserved names actual refused, uninspected or unknown nodes.
	Preserved []string
	// Notes states the remaining phase boundary without claiming completion.
	Notes []string
}

// RemoveHostStateContext removes only validated known metadata after original
// forget. A matching receipt intent and verified operation scope are required.
// A nested call queues finalisation in the outer verified scope. Read the
// returned FinalResult after that outer scope returns.
func RemoveHostStateContext(ctx context.Context, original Host, expectedHostID string, validators map[string]func([]byte) error) (result HostStateRemovalResult, err error) {
	intent, _ := ctx.Value(hostForgetIntentKey{}).(*hostForgetIntent)
	if intent == nil || !SameEntry(intent.original, original) || intent.hostID != expectedHostID {
		return result, hostStateRemovalError("the original state cleanup identity is not bound to its receipt")
	}
	err = WithHostForgetCommittedContext(ctx, original, func(verified context.Context) error {
		var failure error
		result, failure = removeHostStateVerified(verified, original, validators)
		return failure
	})
	if result.outcome != nil {
		outcome := result.outcome
		result = result.FinalResult()
		result.outcome = outcome
	}
	return result, err
}

func removeHostStateVerified(ctx context.Context, original Host, validators map[string]func([]byte) error) (HostStateRemovalResult, error) {
	return prepareHostTerminalRemoval(ctx, original, validators)
}

func withHostStateSet(ctx context.Context, subdirs []string, index int, held []*stateFileTxn, run func([]*stateFileTxn) error) error {
	if index == len(hostStateFiles) {
		return run(held)
	}
	return withStateTransaction(ctx, subdirs, hostStateFiles[index], false, true, func(tx *stateFileTxn) error {
		return withHostStateSet(ctx, subdirs, index+1, append(held, tx), run)
	})
}

func validateHostStateOwner(ctx context.Context, tx *stateFileTxn, validate func([]byte) error) error {
	if err := tx.removalOwnership(); err != nil {
		return err
	}
	view := tx.view()
	raw, err := io.ReadAll(io.NewSectionReader(view.ReaderAt, 0, view.Size))
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	validation := validate(raw)
	if err := ctx.Err(); err != nil {
		return err
	}
	// Expected owner retention is meaningful only while the exact native
	// descriptor/root/sentinel snapshot still matches after that decoding.
	if err := tx.removalOwnership(); err != nil {
		return err
	}
	return validation
}

func compactHostStatePaths(paths []string) []string { slices.Sort(paths); return slices.Compact(paths) }
func knownHostStateEntry(name string) bool {
	for _, known := range hostStateFiles {
		if name == known || name == known+".lock" {
			return true
		}
	}
	return false
}
func hostStateRemovalError(message string) error {
	return &UserError{Msg: message, Hint: "Inspect the saved-host list and the original removal receipt before retrying local cleanup.", Code: "host_state", Kind: KindRefused}
}

func (tx *stateFileTxn) removalOwnership() error {
	if err := tx.check(); err != nil {
		return err
	}
	if tx.lock == nil || tx.source == nil {
		return badGrantStateError(tx.name)
	}
	for _, anchor := range tx.anchors {
		if err := tx.ctx.Err(); err != nil {
			return err
		}
		r, err := os.OpenRoot(anchor.path)
		if err != nil {
			return badGrantStateError(tx.name)
		}
		f, err := r.Open(".")
		if err != nil {
			_ = r.Close()
			return badGrantStateError(tx.name)
		}
		info, err := f.Stat()
		owned := err == nil && os.SameFile(anchor.info, info) && stateSafeRoot(tx.ctx, r) == nil
		_ = f.Close()
		_ = r.Close()
		if stopped := tx.ctx.Err(); stopped != nil {
			return stopped
		}
		if !owned {
			return badGrantStateError(tx.name)
		}
	}
	if _, err := stateSafeDescriptor(tx.ctx, tx.source); err != nil {
		return err
	}
	if _, err := stateSafeDescriptor(tx.ctx, tx.lock.file); err != nil {
		return err
	}
	return tx.check()
}

func hostStateLeftovers(ctx context.Context, root *os.Root, folder string, anchors []stateRootAnchor) ([]string, error) {
	if err := stateRootAnchorsMatch(ctx, root, anchors, "host state"); err != nil {
		return nil, err
	}
	f, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var paths []string
	for {
		if err := ctx.Err(); err != nil {
			return paths, err
		}
		entries, err := f.ReadDir(64)
		for _, entry := range entries {
			if stopped := ctx.Err(); stopped != nil {
				return paths, stopped
			}
			paths = append(paths, filepath.Join(folder, entry.Name()))
		}
		if errors.Is(err, io.EOF) {
			return paths, stateRootAnchorsMatch(ctx, root, anchors, "host state")
		}
		if err != nil {
			return paths, err
		}
	}
}
