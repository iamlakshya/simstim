package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/iamlakshya/simstim/internal/brand"
)

// loginWrapperScope belongs to one CheckLogin stage, never just its name.
// Its temporary paths reuse that stage's existing random private filename.
type loginWrapperScope struct {
	ctx         context.Context
	owner, base string
	mu          sync.Mutex
	allowed     map[string]bool
	files       map[string]ownedSSHConfig
	closed      bool
}

func beginLoginWrapperScope(ctx context.Context, h Host) (*loginWrapperScope, error) {
	unlock, err := lockPin(ctx, h.Name)
	if err != nil {
		return nil, err
	}
	defer unlock()
	st, err := stageForContext(ctx, h.Name)
	if err != nil {
		return nil, err
	}
	if st == nil {
		return nil, errors.New("the login check has no owned host key stage")
	}
	scope := &loginWrapperScope{ctx: ctx, owner: h.Name, base: st.path + "-config",
		allowed: map[string]bool{}, files: map[string]ownedSSHConfig{}}
	stagesMu.Lock()
	st.checkWrappers = scope
	stagesMu.Unlock()
	// The pin lock keeps the active operation stable while the one current
	// variant accessor derives every allowed path from the scoped base.
	paths := hostWrapperPaths(h)
	scope.mu.Lock()
	for _, path := range paths {
		if path != "" {
			scope.allowed[path] = true
		}
	}
	scope.mu.Unlock()
	if err := sameStage(h.Name, st); err != nil {
		return nil, err
	}
	return scope, nil
}

func loginWrapperPath(name string) (string, bool) {
	stagesMu.Lock()
	defer stagesMu.Unlock()
	st := stages[name]
	if st == nil || st.closed || st.checkWrappers == nil {
		return "", false
	}
	return st.checkWrappers.base, true
}

// loginWrapperWriteHooks binds a write to its active stage-owned paths.
// A matching pathname alone never adopts an existing file for cleanup.
func loginWrapperWriteHooks(path, what string) (atomicFileHooks, error) {
	quoted, ok := strings.CutPrefix(what, "host ")
	if !ok {
		return atomicFileHooks{}, nil
	}
	name, err := strconv.Unquote(quoted)
	if err != nil {
		return atomicFileHooks{}, fmt.Errorf("identify the host for the SSH config: %w\nTry: run %s setup again", err, brand.Name)
	}
	stagesMu.Lock()
	st := stages[name]
	var scope *loginWrapperScope
	if st != nil && !st.closed {
		scope = st.checkWrappers
	}
	stagesMu.Unlock()
	if scope == nil {
		return atomicFileHooks{}, nil
	}
	if err := scope.guardDestination(path, nil, ""); err != nil {
		return atomicFileHooks{}, err
	}
	return atomicFileHooks{
		beforeReplace: func(root *os.Root, base string) error {
			return scope.guardDestination(path, root, base)
		},
		published: func(info os.FileInfo) {
			scope.mu.Lock()
			defer scope.mu.Unlock()
			if !scope.closed {
				scope.files[path] = ownedSSHConfig{path: path, owner: scope.owner, info: info}
			}
		},
	}, nil
}

func (s *loginWrapperScope) guardDestination(path string, root *os.Root, base string) error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	allowed, closed := s.allowed[path], s.closed
	previous, had := s.files[path]
	s.mu.Unlock()
	if !allowed || closed {
		return changedLoginWrapperError()
	}
	var info os.FileInfo
	var err error
	if root != nil {
		info, err = root.Lstat(base)
	} else {
		info, err = os.Lstat(path)
	}
	if errors.Is(err, os.ErrNotExist) && !had {
		return nil
	}
	if err != nil || !had || !info.Mode().IsRegular() || !os.SameFile(previous.info, info) {
		return changedLoginWrapperError()
	}
	current, ok := snapshotGeneratedSSHConfig(path)
	if !ok || current.owner != s.owner || !os.SameFile(info, current.info) {
		return changedLoginWrapperError()
	}
	return nil
}

func changedLoginWrapperError() error {
	return &UserError{Msg: "a temporary SSH config changed during the login check, so the client left it alone",
		Hint: brand.Name + " setup", Code: "ssh_config_changed"}
}

func (s *loginWrapperScope) cleanup(ctx context.Context) {
	// Cancellation ends the login, not its bounded cleanup. The same
	// captured inode and current-reference checks still protect every file.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stageAbortWait)
	defer cancel()
	if err := s.remove(ctx); err != nil {
		DebugLog().Warn("could not clean the login check's temporary SSH configs", "err", err)
	}
}

func (s *loginWrapperScope) remove(ctx context.Context) error {
	unlockPin, err := lockPin(ctx, s.owner)
	if err != nil {
		return err
	}
	defer unlockPin()
	s.mu.Lock()
	s.closed = true
	files := make([]ownedSSHConfig, 0, len(s.files))
	for _, candidate := range s.files {
		files = append(files, candidate)
	}
	s.mu.Unlock()
	unlockHosts, err := lockHostsFile(ctx)
	if err != nil {
		return err
	}
	defer unlockHosts()
	hosts, err := LoadHosts()
	if err != nil {
		return err
	}
	for _, candidate := range files {
		if filepath.Dir(candidate.path) != filepath.Dir(s.base) {
			return changedLoginWrapperError()
		}
		if err := unlinkOwnedSSHConfig(ctx, candidate, hosts); err != nil {
			return err
		}
	}
	return ctx.Err()
}
