package host

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const agentLinkWait = 3 * time.Second

// A test may observe contention without changing a process-global lock hook.
type agentLinkContentionKey struct{}

// agentLease owns one publication, not just a target string. Two bridges
// can target the same socket, so cleanup also checks the symlink's inode.
type agentLease struct {
	root         *os.Root
	name         string
	alias        string
	target       string
	info         os.FileInfo
	aliasInfo    os.FileInfo
	aliasWitness string
	linkWitness  string
	temp         string
	once         sync.Once
	done         chan struct{}
	err          error
}

func agentRuntimeAtPath(root *os.Root) bool {
	opened, err := root.Stat(".")
	current, cerr := os.Lstat(filepath.Clean(root.Name()))
	return err == nil && cerr == nil && os.SameFile(opened, current)
}

func (l *agentLease) Close() error { return l.CloseContext(context.Background()) }

func (l *agentLease) CloseContext(parent context.Context) error {
	if l == nil {
		return nil
	}
	l.once.Do(func() {
		defer close(l.done)
		defer l.root.Close()
		ctx, cancel := context.WithTimeout(parent, agentLinkWait)
		defer cancel()
		id := l.name[len("agent-") : len(l.name)-len(".sock")]
		unlock, err := lockAgentLink(ctx, l.root, id)
		if err != nil {
			l.err = err
			return
		}
		defer unlock()
		l.err = l.removeOwnedLink(l.name, l.alias, l.info)
		if l.temp != "" {
			if err := l.removeOwnedLink(l.temp, l.alias, l.info); l.err == nil {
				l.err = err
			}
		}
		if err := l.removeOwnedLink(l.alias, l.target, l.aliasInfo); l.err == nil {
			l.err = err
		}
		if l.err == nil {
			l.err = l.removePublicationWitnesses(ctx)
		}
	})
	return l.err
}

func (l *agentLease) removePublicationWitnesses(ctx context.Context) error {
	for _, entry := range []struct {
		name string
		info os.FileInfo
	}{{l.linkWitness, l.info}, {l.aliasWitness, l.aliasInfo}} {
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.name == "" {
			continue
		}
		current, err := l.root.Lstat(entry.name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !os.SameFile(current, entry.info) {
			return agentSocketError()
		}
		if err := l.root.Remove(entry.name); err != nil && !errors.Is(err, os.ErrNotExist) {
			return agentSocketError()
		}
	}
	return nil
}

func (l *agentLease) removeOwnedLink(name, target string, info os.FileInfo) error {
	if info == nil {
		return nil
	}
	st, err := l.root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return agentSocketError()
	}
	if st.Mode()&os.ModeSymlink == 0 || !os.SameFile(info, st) {
		return nil
	}
	current, err := l.root.Readlink(name)
	if err != nil {
		return agentSocketError()
	}
	if current != target {
		return nil
	}
	if err := l.root.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return agentSocketError()
	}
	return nil
}

func lockAgentLink(ctx context.Context, r *os.Root, id string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	name := "agent-" + id + ".lock"
	if st, err := r.Lstat(name); err == nil && !privateAgentLock(st) {
		return nil, agentSocketError()
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, agentSocketError()
	}
	f, err := r.OpenFile(name, os.O_RDWR|os.O_CREATE|agentNoFollow|agentNonblock, 0o600)
	if err != nil {
		return nil, agentSocketError()
	}
	before, err := r.Lstat(name)
	opened, serr := f.Stat()
	if err != nil || serr != nil || !privateAgentLock(before) || !os.SameFile(before, opened) {
		_ = f.Close()
		return nil, agentSocketError()
	}
	for {
		ok, err := tryAgentLock(f)
		if err != nil {
			_ = f.Close()
			return nil, agentSocketError()
		}
		if ok {
			if err := ctx.Err(); err != nil {
				_ = releaseAgentLock(f)
				_ = f.Close()
				return nil, err
			}
			return func() { _ = releaseAgentLock(f); _ = f.Close() }, nil
		}
		if observe, ok := ctx.Value(agentLinkContentionKey{}).(func()); ok {
			observe()
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, fmt.Errorf("wait for the agent socket lock: %w", ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
}
