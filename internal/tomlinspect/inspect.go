package tomlinspect

import (
	"context"
	"errors"
	"io"
	"reflect"
	"sync"
)

// SchemaVisitor receives FINAL borrowed objects. Enter methods may walk
// their supplied table/array cursor and visit children known to the schema.
// The whole input has already passed native grammar/object validation,
// including unknown/discarded values. No cursor may escape its inspection.
type SchemaVisitor interface {
	EnterTable(PathCursor, TableCursor) error
	LeaveTable(PathCursor) error
	EnterArray(PathCursor, ArrayCursor) error
	LeaveArray(PathCursor) error
	Scalar(PathCursor, ScalarCursor) error
	Finish() error
}

// Inspector owns fixed reusable state and captures only the actual mode's
// explicit resource capability. It retains no parsed value or permission
// result after Inspect returns. One mode factory serialises all slab use.
type Inspector struct {
	outer     inspectionState
	factory   IndexFactory
	source    source
	store     indexStore
	stack     lexStack
	lexer     lexer
	parser    metadataParser
	epoch     uint64
	active    bool
	statsMu   sync.Mutex
	lastStats MetadataStats
}

// NewInspector refuses a missing resource owner. It creates no directory,
// file, process, listener or fallback provider.
func NewInspector(factory IndexFactory) (*Inspector, error) {
	if nilInterface(factory) {
		return nil, ErrIndexInvariant
	}
	return &Inspector{factory: factory}, nil
}

// Inspect validates the complete pinned input and then its final application
// schema. The caller owns the pinned regular snapshot and its before/after
// mutation checks. Metadata resources are closed before success is returned.
// This internal API is not an outbound permission or general Settings value.
func (i *Inspector) Inspect(ctx context.Context, input io.ReaderAt, size int64, visitor SchemaVisitor) (err error) {
	if i == nil || i.factory == nil || ctx == nil || nilInterface(input) || nilInterface(visitor) {
		return ErrIndexInvariant
	}
	if size < 0 || size > MaxInput {
		return ErrInputSize
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	lease, err := i.factory.AcquireIndex(ctx, size)
	if err != nil {
		if !nilInterface(lease) {
			return errors.Join(err, lease.Close())
		}
		return err
	}
	if nilInterface(lease) {
		return ErrIndexInvariant
	}
	defer func() {
		if cause := recover(); cause != nil {
			if failure, ok := cause.(error); ok {
				err = failure
			} else {
				i.detach()
				_ = lease.Close()
				panic(cause)
			}
		}
		if err == nil {
			if stopped := ctx.Err(); stopped != nil {
				err = stopped
			} else {
				file := lease.File()
				if nilInterface(file) {
					err = ErrIndexInvariant
				} else if info, failure := file.Stat(); failure != nil {
					err = failure
				} else if info == nil || !info.Mode().IsRegular() {
					err = ErrIndexInvariant
				}
			}
		}
		// No source/page view remains live when Close clears/releases the
		// serial slab. Close must report a mode/caller revocation racing
		// after the final guarded Stat as well as actual cleanup failure.
		if err != nil {
			i.completeErrorPosition(err)
		}
		i.recordStats(size)
		i.detach()
		if failure := lease.Close(); failure != nil {
			err = errors.Join(err, failure)
		}
	}()
	i.store = indexStore{}
	workspace := lease.Workspace()
	if len(workspace) != IndexWorkspaceBytes || nilInterface(lease.File()) {
		return ErrIndexInvariant
	}
	i.epoch++
	if i.epoch == 0 {
		return ErrIndexInvariant
	}
	i.active = true
	i.source = source{ctx: ctx, input: input, size: size, generation: i.epoch}
	if err := i.stack.reset(workspace); err != nil {
		return err
	}
	if err := i.store.reset(ctx, lease.File(), workspace, &i.source); err != nil {
		return err
	}
	resetLexer(&i.lexer, &i.source, &i.stack)
	i.parser = metadataParser{lx: &i.lexer, store: &i.store}
	root, err := i.parser.run()
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	path := PathCursor{inspector: i, epoch: i.epoch, scope: root.object}
	value := ValueCursor{inspector: i, epoch: i.epoch, path: path, value: root}
	if err := value.Visit(visitor); err != nil {
		return err
	}
	return visitor.Finish()
}

func (i *Inspector) detach() {
	i.active = false
	i.parser = metadataParser{}
	i.lexer = lexer{}
	i.stack = lexStack{}
	i.source = source{}
	counters := i.store.counters
	i.store = indexStore{indexPages: indexPages{counters: counters}}
}

func nilInterface(input any) bool {
	if input == nil {
		return true
	}
	value := reflect.ValueOf(input)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	}
	return false
}

func (i *Inspector) borrowed(epoch uint64) error {
	if i == nil || !i.active || i.epoch != epoch || i.source.ctx == nil {
		return ErrIndexInvariant
	}
	return i.source.ctx.Err()
}

func cursorError(err *error) {
	if cause := recover(); cause != nil {
		if failure, ok := cause.(error); ok {
			*err = failure
			return
		}
		panic(cause)
	}
}
