package client

import (
	"path/filepath"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

func checkHostTerminalInventory(expected, current hostTerminalHeader, complete bool) error {
	if expected.Name != current.Name || expected.Key != current.Key || expected.OP != current.OP || expected.Parent != current.Parent {
		return hostStateRemovalError("the terminal checkpoint native OP or parent changed")
	}
	if !terminalFacetMatches(expected.Folder, current.Folder, complete) {
		return hostStateRemovalError("the terminal checkpoint folder changed or appeared")
	}
	for i := range expected.Data {
		if !terminalReferenceMatches(expected.Data[i], current.Data[i], complete) || !terminalReferenceMatches(expected.Locks[i], current.Locks[i], complete) {
			return hostStateRemovalError("a terminal state node changed, appeared or cannot be reconstructed")
		}
	}
	if !terminalReferenceMatches(expected.Who, current.Who, complete) {
		return hostStateRemovalError("the terminal diagnostic changed, appeared or cannot be reconstructed")
	}
	return nil
}

func terminalFacetMatches(expected, current *hostTerminalFacet, allowRemoved bool) bool {
	if current == nil {
		return expected == nil || allowRemoved
	}
	return expected != nil && *expected == *current
}

func terminalReferenceMatches(expected, current *cachefs.Reference, allowRemoved bool) bool {
	if current == nil {
		return expected == nil || allowRemoved
	}
	return expected != nil && expected.Known() && current.Known() && expected.Version == current.Version &&
		expected.Identity == current.Identity && expected.Mode == current.Mode && expected.Size == current.Size &&
		expected.Modified.Equal(current.Modified) && expected.Directory == current.Directory
}

func terminalRemainingPaths(expected, current hostTerminalHeader) []string {
	folder := removalDir(expected.Name)
	paths := []string{hostLockPath(expected.Name)}
	if current.Folder != nil {
		paths = append(paths, folder)
	}
	for i, name := range hostStateFiles {
		if current.Data[i] != nil {
			paths = append(paths, filepath.Join(folder, name))
		}
		if current.Locks[i] != nil {
			paths = append(paths, filepath.Join(folder, name+".lock"))
		}
	}
	if current.Who != nil {
		paths = append(paths, hostLockPath(expected.Name)+".who")
	}
	return compactHostStatePaths(paths)
}
