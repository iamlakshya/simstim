package client

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

func (p *hostTerminalPlan) check(ctx context.Context, generation string) error {
	if p.frame.header.Target != generation || p.op.gone.Load() || !p.op.terminal.Load() || p.authority == nil {
		return hostStateRemovalError("the terminal epilogue lost its original exclusive operation")
	}
	if err := p.authority(ctx); err != nil {
		return err
	}
	if err := p.op.native.check(ctx); err != nil {
		return err
	}
	return p.checkpoint.CheckFile(p.op.native.root, p.op.native.name, p.op.native.file, 0600)
}

func (p *hostTerminalPlan) finalise(ctx context.Context, generation string) (failure error) {
	result := (HostStateRemovalResult{outcome: p.outcome}).FinalResult()
	result.Notes = nil
	defer func() {
		if failure != nil {
			if slices.Contains(result.Retained, hostLockPath(p.frame.header.Name)) {
				result.Notes = []string{"The immutable OP checkpoint remains the retry authority for incomplete local retirement."}
			} else {
				result.Notes = []string{"Local node removal committed, but its final durability or handle closure returned an error."}
			}
		}
		result.Removed = compactHostStatePaths(result.Removed)
		result.Retained = compactHostStatePaths(result.Retained)
		result.Preserved = compactHostStatePaths(result.Preserved)
		p.resolve(result)
	}()
	if err := p.check(ctx, generation); err != nil {
		return err
	}
	header := p.frame.header
	parent := p.op.native.root
	if err := checkHostTerminalRecordedFile(ctx, parent, p.op.native.name+".who", header.Who); err != nil {
		terminalPreserved(&result, hostLockPath(header.Name)+".who")
		return err
	}
	folder, err := p.openFolder(ctx)
	if err != nil {
		terminalPreserved(&result, removalDir(header.Name))
		return err
	}
	if folder != nil {
		defer folder.Close()
		// Every present/absent fact and the whole inventory are checked before
		// the first delete. Old tx views and handles have already been closed.
		if err := p.checkFolderInventory(ctx, folder); err != nil {
			terminalPreserved(&result, removalDir(header.Name))
			return err
		}
		for i, name := range hostStateFiles {
			if err := p.removeTerminalFile(ctx, generation, folder, name, header.Data[i], filepath.Join(removalDir(header.Name), name), &result); err != nil {
				return err
			}
		}
		for i, name := range hostStateFiles {
			if err := p.removeTerminalFile(ctx, generation, folder, name+".lock", header.Locks[i], filepath.Join(removalDir(header.Name), name+".lock"), &result); err != nil {
				return err
			}
		}
		if err := p.check(ctx, generation); err != nil {
			return err
		}
		if err := terminalEmptyFolder(ctx, folder); err != nil {
			terminalPreserved(&result, removalDir(header.Name))
			return err
		}
		fresh, err := cachefs.CaptureRoot(folder)
		if err != nil || header.Folder == nil || terminalFacet(fresh) != *header.Folder {
			return hostStateRemovalError("the terminal folder changed before native closure")
		}
		// Close all owned folder handles before native rmdir, including on the
		// Windows fallback where deleted children can remain delete-pending.
		if err := folder.Close(); err != nil {
			return err
		}
		if err := p.checkNamedFolder(ctx); err != nil {
			return err
		}
		beforeHostStateUnlink(header.Key)
		if err := p.check(ctx, generation); err != nil {
			return err
		}
		if err := p.checkNamedFolder(ctx); err != nil {
			return err
		}
		if err := parent.Remove(header.Key); err != nil {
			return err
		}
		terminalRemoved(&result, removalDir(header.Name))
		if err := syncStateDirectory(context.WithoutCancel(ctx), parent); err != nil {
			return err
		}
	}
	if err := p.removeTerminalFile(ctx, generation, parent, p.op.native.name+".who", header.Who, hostLockPath(header.Name)+".who", &result); err != nil {
		return err
	}
	beforeHostStateUnlink(p.op.native.name)
	if err := p.check(ctx, generation); err != nil {
		return err
	}
	// No worker can remain: the exclusive gate drained ordinary users before
	// membership, and the outer scope joined every nested worker before us.
	// Mark the old logical owner ended before its LAST destructive effect.
	p.op.gone.Store(true)
	if err := parent.Remove(p.op.native.name); err != nil {
		p.op.stateLifetime().beginClose()
		return err
	}
	terminalRemoved(&result, hostLockPath(header.Name))
	// From here on there are only owned closes and parent durability sync.
	// A later deferred unlock sees terminal mode and deletes nothing.
	closeErr := p.op.native.file.Close()
	syncErr := syncStateDirectory(context.WithoutCancel(ctx), parent)
	rootErr := parent.Close()
	stopped := ctx.Err()
	// Closing admission cancels only operation-derived transaction contexts.
	// Capture caller cancellation before this intentional logical shutdown.
	p.op.stateLifetime().beginClose()
	return errors.Join(stopped, closeErr, syncErr, rootErr)
}

func (p *hostTerminalPlan) openFolder(ctx context.Context) (*os.Root, error) {
	folder, err := cachefs.OpenPrivateChild(p.op.native.root, p.frame.header.Key, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	fresh, err := cachefs.CaptureRoot(folder)
	if err != nil || p.frame.header.Folder == nil || terminalFacet(fresh) != *p.frame.header.Folder {
		_ = folder.Close()
		return nil, hostStateRemovalError("the terminal folder changed or appeared after checkpointing")
	}
	return folder, ctx.Err()
}

func (p *hostTerminalPlan) checkNamedFolder(ctx context.Context) error {
	root, err := p.openFolder(ctx)
	if err != nil {
		return err
	}
	if root == nil {
		return hostStateRemovalError("the terminal folder disappeared before its exact rmdir")
	}
	defer root.Close()
	return terminalEmptyFolder(ctx, root)
}

func (p *hostTerminalPlan) removeTerminalFile(ctx context.Context, generation string, root *os.Root, name string, expected *cachefs.Reference, path string, result *HostStateRemovalResult) error {
	beforeHostStateUnlink(name)
	if err := p.check(ctx, generation); err != nil {
		return err
	}
	if knownHostStateEntry(name) {
		if err := p.checkFolderBinding(ctx, root); err != nil {
			terminalPreserved(result, removalDir(p.frame.header.Name))
			return err
		}
	}
	file, err := cachefs.OpenFile(root, name, os.O_RDONLY, 0600)
	if errors.Is(err, os.ErrNotExist) {
		result.Retained = slices.DeleteFunc(result.Retained, func(existing string) bool { return existing == path })
		return nil
	}
	if err != nil {
		terminalPreserved(result, path)
		return err
	}
	defer file.Close()
	if expected == nil || expected.CheckFile(root, name, file, 0600) != nil {
		terminalPreserved(result, path)
		return hostStateRemovalError("a terminal node changed or appeared before its exact unlink")
	}
	for i, dataName := range hostStateFiles {
		if name == dataName {
			if err := checkHostTerminalRequiredFile(ctx, root, name+".lock", p.frame.header.Locks[i]); err != nil {
				terminalPreserved(result, path)
				terminalPreserved(result, path+".lock")
				return err
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := root.Remove(name); err != nil {
		terminalPreserved(result, path)
		return err
	}
	terminalRemoved(result, path)
	// Close this exact handle before a possible subsequent folder rmdir.
	closeErr := file.Close()
	flushErr := syncStateDirectory(context.WithoutCancel(ctx), root)
	if err := ctx.Err(); err != nil {
		return errors.Join(err, closeErr, flushErr)
	}
	return errors.Join(closeErr, flushErr)
}

func terminalRemoved(result *HostStateRemovalResult, path string) {
	result.Removed = append(result.Removed, path)
	result.Retained = slices.DeleteFunc(result.Retained, func(existing string) bool { return existing == path })
}

func (p *hostTerminalPlan) checkFolderInventory(ctx context.Context, root *os.Root) error {
	file, err := root.Open(".")
	if err != nil {
		return err
	}
	defer file.Close()
	for {
		entries, err := file.ReadDir(64)
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !knownHostStateEntry(entry.Name()) {
				return hostStateRemovalError("unknown host state appeared after the immutable checkpoint")
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
	}
	for i, name := range hostStateFiles {
		for _, row := range []struct {
			name string
			ref  *cachefs.Reference
		}{{name, p.frame.header.Data[i]}, {name + ".lock", p.frame.header.Locks[i]}} {
			current, err := cachefs.OpenFile(root, row.name, os.O_RDONLY, 0600)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			check := error(nil)
			if row.ref == nil {
				check = hostStateRemovalError("recorded-absent terminal state appeared")
			} else {
				check = row.ref.CheckFile(root, row.name, current, 0600)
			}
			closeErr := current.Close()
			if check != nil || closeErr != nil {
				return errors.Join(check, closeErr)
			}
		}
	}
	return ctx.Err()
}

func terminalEmptyFolder(ctx context.Context, root *os.Root) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	file, err := root.Open(".")
	if err != nil {
		return err
	}
	defer file.Close()
	entries, err := file.ReadDir(1)
	if len(entries) != 0 {
		return hostStateRemovalError("new or unknown local state kept the terminal folder nonempty")
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return ctx.Err()
}

func checkHostTerminalRecordedFile(ctx context.Context, root *os.Root, name string, reference *cachefs.Reference) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	file, err := cachefs.OpenFile(root, name, os.O_RDONLY, 0600)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	if reference == nil {
		return hostStateRemovalError("recorded-absent terminal state appeared before deletion")
	}
	return reference.CheckFile(root, name, file, 0600)
}

func (p *hostTerminalPlan) checkFolderBinding(ctx context.Context, root *os.Root) error {
	fresh, err := cachefs.CaptureRoot(root)
	if err != nil || p.frame.header.Folder == nil || terminalFacet(fresh) != *p.frame.header.Folder {
		return hostStateRemovalError("the terminal native folder changed before unlink")
	}
	ambient, err := p.openFolder(ctx)
	if err != nil {
		return err
	}
	if ambient == nil {
		return hostStateRemovalError("the terminal folder name disappeared before unlink")
	}
	return ambient.Close()
}

func checkHostTerminalRequiredFile(ctx context.Context, root *os.Root, name string, reference *cachefs.Reference) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	file, err := cachefs.OpenFile(root, name, os.O_RDONLY, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	if reference == nil {
		return hostStateRemovalError("the terminal data has no original sentinel witness")
	}
	return reference.CheckFile(root, name, file, 0600)
}

func terminalPreserved(result *HostStateRemovalResult, path string) {
	result.Preserved = append(result.Preserved, path)
	result.Retained = slices.DeleteFunc(result.Retained, func(existing string) bool {
		if existing == path || strings.HasPrefix(existing, path+string(filepath.Separator)) {
			result.Preserved = append(result.Preserved, existing)
			return true
		}
		return false
	})
}
