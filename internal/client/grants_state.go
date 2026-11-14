package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
)

// errStateNoChange tells withStateFile to leave the state file untouched.
var errStateNoChange = errors.New("state unchanged")

// beforeGrantsStateWrite runs inside withStateFile right before the atomic
// write. Tests use it to swap parent directories mid-flight.
var beforeGrantsStateWrite = func() {}

// afterStateFilePublish observes publication while both descriptors remain owned.
var afterStateFilePublish = func() {}

var (
	stateLocksMu sync.Mutex
	stateLocks   = map[string]*sync.Mutex{}
)

func stateProcessMutex(subdirs []string, fileName string) *sync.Mutex {
	key := filepath.Join(StateDir(), strings.Join(subdirs, "/"), fileName)
	stateLocksMu.Lock()
	defer stateLocksMu.Unlock()
	mu := stateLocks[key]
	if mu == nil {
		mu = &sync.Mutex{}
		stateLocks[key] = mu
	}
	return mu
}

func badGrantStateError(what string) error {
	return &UserError{
		Msg:  fmt.Sprintf("the local state file %s cannot be read or written safely", what),
		Hint: "Check the files and permissions under " + StateDir() + ", then try again.",
		Code: "grant_state",
		Kind: KindRefused,
	}
}

func wrapGrantStateError(what string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var ue *UserError
	if errors.As(err, &ue) {
		return err
	}
	return badGrantStateError(what)
}

// withStateFile retains the original byte callback semantics over the shared
// descriptor transaction. A nil byte result still writes an empty file.
func withStateFile(ctx context.Context, subdirs []string, name string, write bool, fn func([]byte, bool) ([]byte, error)) error {
	return withStateFileFD(ctx, subdirs, name, write, func(view stateFileView) (stateFileReplace, error) {
		var raw []byte
		if view.Exists {
			var err error
			raw, err = io.ReadAll(io.NewSectionReader(view.ReaderAt, 0, view.Size))
			if err != nil {
				return nil, wrapGrantStateError(name, err)
			}
		}
		next, err := fn(raw, view.Exists)
		if err != nil {
			return nil, err
		}
		if !write {
			return nil, nil
		}
		return func(ctx context.Context, w io.Writer) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			_, err := w.Write(next)
			return wrapGrantStateError(name, err)
		}, nil
	})
}
