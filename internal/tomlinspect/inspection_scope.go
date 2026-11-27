package tomlinspect

import (
	"context"
	"errors"
)

// WithSources owns ONE complete admitted lease for actual settings and state
// sources. Success means the real callback completed its checks, not a policy
// permission. The actual caller must run the complete canonical P23 validator.
// No fallback to the legacy one-source factory is allowed.
func (i *Inspector) WithSources(ctx context.Context, sources []MetadataSource, visit func(*Inspection) error) (err error) {
	if i == nil || ctx == nil || visit == nil || len(sources) != 2 {
		return ErrIndexInvariant
	}
	factory, ok := i.factory.(SourceIndexFactory)
	if !ok || nilInterface(factory) {
		return ErrIndexInvariant
	}
	var original [2]MetadataSource
	for n, src := range sources {
		if nilInterface(src) || src.Size() < 0 || src.Kind() != uint8(n+1) {
			return ErrIndexInvariant
		}
		if n == 0 && src.Size() > MaxInput {
			return ErrInputSize
		}
		original[n] = src
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	lease, err := factory.AcquireSources(ctx, original[:])
	if err != nil {
		if !nilInterface(lease) {
			return errors.Join(err, lease.Close())
		}
		return err
	}
	if nilInterface(lease) {
		return ErrIndexInvariant
	}
	i.store = indexStore{}
	defer func() {
		if cause := recover(); cause != nil {
			if failure, ok := cause.(error); ok {
				err = failure
			} else {
				i.detachScope()
				i.detach()
				_ = lease.Close()
				panic(cause)
			}
		}
		if err == nil {
			err = i.outer.failure
		}
		if err == nil {
			err = i.outer.regions.failure
		}
		if err == nil && (!i.outer.tomlDone || i.outer.object.active || i.outer.object.pending.id != 0 || i.outer.stateActive || i.outer.entryActive || i.outer.pendingState.id != 0 || i.outer.pendingEntry.id != 0 || i.outer.countActive) {
			err = ErrIndexInvariant
		}
		if err == nil {
			err = ctx.Err()
		}
		if err == nil {
			for _, src := range original {
				if failure := src.Check(ctx); failure != nil {
					err = failure
					break
				}
			}
		}
		if err == nil {
			file := lease.File()
			if nilInterface(file) {
				err = ErrIndexInvariant
			} else if info, failure := file.Stat(); failure != nil {
				err = failure
			} else if info == nil || !info.Mode().IsRegular() {
				err = ErrIndexInvariant
			}
		}
		if err != nil {
			i.completeErrorPosition(err)
		}
		i.recordStats(original[0].Size())
		// All sources/region views are revoked before the actual lease may
		// clear/reuse the slab. Main's callback already joined its merge.
		i.detachScope()
		i.detach()
		if failure := lease.Close(); failure != nil {
			err = errors.Join(err, failure)
		}
	}()
	workspace := lease.Workspace()
	file := lease.File()
	if len(workspace) != IndexWorkspaceBytes || nilInterface(file) {
		return ErrIndexInvariant
	}
	for _, src := range original {
		if err := src.Check(ctx); err != nil {
			return err
		}
	}
	i.epoch++
	if i.epoch == 0 {
		return ErrIndexInvariant
	}
	regionEpoch := i.outer.regions.epoch
	i.outer = inspectionState{ctx: ctx, sources: original, workspace: workspace, file: file, epoch: i.epoch, active: true, regions: regionPages{epoch: regionEpoch}}
	i.active = true
	i.source = source{ctx: ctx, input: original[0], size: original[0].Size(), generation: i.epoch}
	i.store = indexStore{}
	if err := i.stack.reset(workspace); err != nil {
		return err
	}
	handle := &Inspection{owner: i, epoch: i.epoch}
	if err := visit(handle); err != nil {
		return i.scopeFailure(err)
	}
	return nil
}

// InspectTOML parses the actual held settings source exactly once. Its whole
// native graph remains live until every state/entry region has completed.
func (h *Inspection) InspectTOML(id int, visitor SchemaVisitor) (err error) {
	i, err := h.check()
	if err != nil {
		return err
	}
	if id != 0 || nilInterface(visitor) || i.outer.tomlDone || i.outer.tomlRunning || i.outer.stateActive || i.outer.countActive {
		return i.scopeFailure(ErrIndexInvariant)
	}
	i.outer.tomlRunning = true
	defer func() {
		i.outer.tomlRunning = false
		if err != nil {
			i.scopeFailure(err)
		}
	}()
	if err = i.store.reset(i.outer.ctx, i.outer.file, i.outer.workspace, &i.source); err != nil {
		return err
	}
	resetLexer(&i.lexer, &i.source, &i.stack)
	i.parser = metadataParser{lx: &i.lexer, store: &i.store}
	root, err := i.parser.run()
	if err != nil {
		return err
	}
	path := PathCursor{inspector: i, epoch: i.epoch, scope: root.object}
	if err = (ValueCursor{inspector: i, epoch: i.epoch, path: path, value: root}).Visit(visitor); err != nil {
		return err
	}
	if err = visitor.Finish(); err != nil {
		return err
	}
	i.outer.tomlBound = uint64(i.store.maxPages)
	i.outer.maxBound = i.outer.tomlBound
	if err = i.outer.regions.reset(&i.store.indexPages); err != nil {
		return err
	}
	i.outer.tomlDone = true
	return nil
}
