//go:build darwin || linux

package client

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func agentIndexOwnedInfo(info os.FileInfo, links uint64) bool {
	if !agentNativeOwnedInfo(info, 0) || info.Mode().Perm() != 0o600 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && uint64(stat.Nlink) == links
}

func (l *agentIndexLease) create() error {
	if err := l.checkLocked(); err != nil {
		return err
	}
	root := l.mode.directory.root
	file, err := root.OpenFile("settings-index", os.O_CREATE|os.O_EXCL|os.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return agentModeResourceError()
	}
	l.file = file
	l.pending = true
	info, err := file.Stat()
	if err != nil || !agentIndexOwnedInfo(info, 1) || info.Size() != 0 {
		return agentModeResourceError()
	}
	l.info = info
	// The open descriptor proves only this inode. A replaced pathname is
	// preserved, and never adopted as an index or removed by cleanup.
	named, err := root.Lstat("settings-index")
	if err != nil || !os.SameFile(named, info) || !agentIndexOwnedInfo(named, 1) {
		return agentModeResourceError()
	}
	if err := root.Remove("settings-index"); err != nil {
		return agentModeResourceError()
	}
	if _, err := root.Lstat("settings-index"); !errors.Is(err, os.ErrNotExist) {
		return agentModeResourceError()
	}
	unlinked, err := file.Stat()
	if err != nil || !agentIndexOwnedInfo(unlinked, 0) || !os.SameFile(unlinked, info) || unlinked.Size() != 0 {
		return agentModeResourceError()
	}
	l.pending = false
	return l.checkLocked()
}

// Only a known empty, exclusively created inode may be retried after an
// interrupted creation. No name observed later can substitute for that inode.
func (l *agentIndexLease) removePending() error {
	if !l.pending || l.info == nil {
		return nil
	}
	root := l.mode.directory.root
	named, err := root.Lstat("settings-index")
	if errors.Is(err, os.ErrNotExist) {
		l.pending = false
		return nil
	}
	if err != nil || !agentIndexOwnedInfo(named, 1) || !os.SameFile(named, l.info) || named.Size() != 0 {
		return agentModeResourceError()
	}
	opened, err := l.file.Stat()
	if err != nil || !agentIndexOwnedInfo(opened, 1) || !os.SameFile(opened, named) || opened.Size() != 0 {
		return agentModeResourceError()
	}
	if err := root.Remove("settings-index"); err != nil {
		return agentModeResourceError()
	}
	if _, err := root.Lstat("settings-index"); !errors.Is(err, os.ErrNotExist) {
		return agentModeResourceError()
	}
	opened, err = l.file.Stat()
	if err != nil || !agentIndexOwnedInfo(opened, 0) || !os.SameFile(opened, l.info) {
		return agentModeResourceError()
	}
	l.pending = false
	return nil
}
