package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/iamlakshya/simstim/internal/brand"
)

// The stage: a command that adds a host keeps its new pin apart, in a
// staged file, until it saves the host with SaveHostPin. Its ssh commands
// check the staged file, and another process sees no pin. A failed add then
// leaves no pin. The stage belongs to the operation that opened it: a pin
// that an operation reads after its stage closed, or after a newer one
// opened, is never written (pinHostKey).

// stage is a pin that a command that adds a host keeps apart until it
// saves the host.
type stage struct {
	path          string
	keys          []hostKey
	closed        bool
	checkWrappers *loginWrapperScope
}

// beforeStagePublish lets a test cancel after the temporary file exists,
// before the stage replaces another operation's state.
var beforeStagePublish = func() {}

var (
	stagesMu sync.Mutex
	stages   = map[string]*stage{}
)

// currentStage is the stage that this process has open for name, or nil.
func currentStage(name string) *stage {
	stagesMu.Lock()
	defer stagesMu.Unlock()
	return stages[name]
}

// staging reports whether this process has a stage open for name: a host
// add, which saves the host itself.
func staging(name string) bool { return currentStage(name) != nil }

// stagedPin is the stage of name that holds a pin, if there is one.
func stagedPin(name string) (*stage, bool) {
	stagesMu.Lock()
	defer stagesMu.Unlock()
	st := stages[name]
	if st == nil || st.closed || len(st.keys) == 0 {
		return nil, false
	}
	snapshot := *st
	snapshot.keys = slices.Clone(st.keys)
	return &snapshot, true
}

// StagePin opens a stage for the host called name, in this process, under
// the pin's lock, so it waits for a pin that is being written. Until
// SaveHostPin or AbortPin, a pin that the process writes for name goes to a
// staged file, and its ssh commands for name check that file. A stage that
// was open before closes.
func StagePin(ctx context.Context, name string) error {
	_, err := openPinStage(ctx, name)
	return err
}

func openPinStage(ctx context.Context, name string) (*stage, error) {
	if !ValidHostName(name) {
		return nil, nil
	}
	unlock, err := lockPin(ctx, name)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir := filepath.Join(ConfigDir(), "ssh")
	f, err := os.CreateTemp(dir, ".known_hosts-"+HostFileKey(name)+"-staged-*")
	if err != nil {
		return nil, fmt.Errorf("stage the pinned host key of %s: %w", name, err)
	}
	_ = f.Close()
	beforeStagePublish()
	stagesMu.Lock()
	if err := ctx.Err(); err != nil {
		stagesMu.Unlock()
		_ = os.Remove(f.Name())
		return nil, err
	}
	old := stages[name]
	st := &stage{path: f.Name()}
	stages[name] = st
	if old != nil {
		old.closed = true
	}
	stagesMu.Unlock()
	if old != nil {
		_ = os.Remove(old.path)
	}
	return st, nil
}

// AbortPin closes the stage of name, if any, and removes its file, under
// the pin's lock: an operation that writes a pin into the stage finishes
// first, and one that has not written it yet never will.
func AbortPin(name string) { abortPinStage(context.Background(), name, currentStage(name)) }

// publishPin moves a staged pin into place. Tests make it fail.
var publishPin = os.Rename

// SaveHostPin saves the current staged pin and one saved host entry.
func SaveHostPin(h Host) error { return SaveHostPinContext(context.Background(), h) }

// SaveHostPinContext saves only the stage owned by this operation, using
// the common host, pin, hosts-file and tier transaction.
func SaveHostPinContext(ctx context.Context, h Host) error {
	was, err := SavedEntry(h.Name)
	if err != nil {
		return err
	}
	return UpdateHosts(ctx, HostChange{Kind: "host save", Was: was, Now: &h, Pin: true})
}

// checkStagedPin checks the pin that UpdateHosts saves with h: the pin
// staged in this process, or the pin that h has already. A host that the
// user adds over SSH is never saved without a pin. A machine that a
// provider made is pinned by the provider, or at its first connection. A
// host that another client saved with another pin meanwhile keeps that
// pin: h is not saved, and the error says to use host rekey. publish moves
// a newly staged pin into place, and returns the rollback that restores
// the old pin when the save of hosts.toml fails.
func checkStagedPin(ctx context.Context, h Host, hosts []Host, st *stage) (publish func() (func(), error), err error) {
	var staged []hostKey
	if err := sameStage(h.Name, st); err != nil {
		return nil, err
	}
	if st != nil {
		stagesMu.Lock()
		staged = slices.Clone(st.keys)
		stagesMu.Unlock()
	}
	isSaved := slices.ContainsFunc(hosts, func(o Host) bool { return o.Name == h.Name })
	path := pinPath(h.Name)
	prev, readErr := os.ReadFile(path)
	if readErr == nil {
		if err := ownPin(h.Name, path, prev); err != nil {
			return nil, err
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return nil, fmt.Errorf("read the pinned host key of %s: %w", h.Name, readErr)
	}
	noop := func() (func(), error) { return nil, nil }
	switch {
	case len(staged) == 0:
		if h.SSH != "" && h.Machine == nil && (readErr != nil || len(readKeys(prev)) == 0) {
			return nil, &UserError{Msg: h.Name + " has no pinned host key, so the client does not save it",
				Hint: fmt.Sprintf("%s host add %s %s", brand.Name, h.Name, ShellWord(h.SSH)), Code: "host_key_unpinned"}
		}
		return noop, nil // no SSH, or the pin that h has already
	case isSaved && readErr == nil:
		old := readKeys(prev)
		if !slices.ContainsFunc(staged, func(k hostKey) bool { return holds(old, k) }) {
			return nil, pinnedOther(h.Name, old, pinOf(staged, "").Fingerprint)
		}
		return noop, nil // the pin that is there holds the key: it stays
	}
	return func() (func(), error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := sameStage(h.Name, st); err != nil {
			return nil, err
		}
		if err := publishPin(st.path, path); err != nil {
			return nil, fmt.Errorf("write the pinned host key of %s: %w", h.Name, err)
		}
		rollback := func() {
			undo := os.Remove(path)
			if readErr == nil {
				undo = replaceFile(path, h.Name, prev)
			}
			if undo != nil {
				DebugLog().Warn("could not put back the pin of before", "host", h.Name, "err", undo)
			}
		}
		return rollback, nil
	}, nil
}
