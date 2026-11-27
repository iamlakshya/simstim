package client

import (
	"context"
	"os"
	"path/filepath"
	"slices"
)

func ownedControlSockets(ctx context.Context) []ownedControlSocket {
	if !controlMaster {
		return nil
	}
	hosts, err := LoadHosts()
	if err != nil {
		return nil
	}
	scope, scoped := controlScope()
	if !scoped {
		return nil
	}
	var found []ownedControlSocket
	for _, host := range hosts {
		if !ValidHostName(host.Name) {
			continue
		}
		err := withControlHost(ctx, host, func(current []Host) error {
			if !slices.ContainsFunc(current, func(h Host) bool { return h.Name == host.Name && SameEntry(h, host) }) {
				return nil
			}
			found = append(found, hostControlSockets(ctx, host, scope, current)...)
			return ctx.Err()
		})
		if err != nil && ctx.Err() != nil {
			break
		}
	}
	return found
}

// hostControlSockets runs entirely under the caller's pin and hosts locks.
func hostControlSockets(ctx context.Context, host Host, scope string, current []Host) []ownedControlSocket {
	var found []ownedControlSocket
	for _, base := range controlDirs() {
		hostRoot, markers := indexedHostControl(host, base, scope)
		for _, marker := range markers {
			epoch, sockets := indexedControlEpoch(hostRoot, marker.Name())
			for _, socket := range sockets {
				if candidate, ok := ownedEpochControlSocket(ctx, epoch, socket.Name(), current); ok {
					found = append(found, candidate)
				}
			}
		}
	}
	return found
}

// indexedHostControl captures each private directory before listing markers.
func indexedHostControl(host Host, base, scope string) (ownedControlSocket, []os.DirEntry) {
	c := ownedControlSocket{host: host, base: base, scope: scope}
	baseInfo, err := os.Lstat(base)
	if err != nil || !privateControlInfo(baseInfo) {
		return c, nil
	}
	top := filepath.Join(base, "h")
	topInfo, err := os.Lstat(top)
	if err != nil || !privateControlInfo(topInfo) {
		return c, nil
	}
	c.hostKey = controlHostKey(host.Name)
	hostDir := filepath.Join(top, c.hostKey)
	hostInfo, err := os.Lstat(hostDir)
	if err != nil || !privateControlInfo(hostInfo) {
		return c, nil
	}
	entries, err := os.ReadDir(hostDir)
	if err != nil {
		return c, nil
	}
	c.baseInfo, c.topInfo, c.hostInfo = baseInfo, topInfo, hostInfo
	return c, entries
}

// indexedControlEpoch accepts only an owned marker and its private epoch.
func indexedControlEpoch(c ownedControlSocket, key string) (ownedControlSocket, []os.DirEntry) {
	if !controlKeyRE.MatchString(key) {
		return c, nil
	}
	token, marker, ok := readOwnedControlMarker(filepath.Join(c.base, "h", c.hostKey, key))
	if !ok {
		return c, nil
	}
	epochInfo, err := os.Lstat(filepath.Join(c.base, token))
	if err != nil || !privateControlInfo(epochInfo) {
		return c, nil
	}
	sockets, err := os.ReadDir(filepath.Join(c.base, token))
	if err != nil {
		return c, nil
	}
	c.key, c.token = key, token
	c.markerInfo, c.epochInfo = marker, epochInfo
	return c, sockets
}

// ownedEpochControlSocket rechecks the captured index and inode before probing.
func ownedEpochControlSocket(ctx context.Context, c ownedControlSocket, name string, current []Host) (ownedControlSocket, bool) {
	if !epochSocketName.MatchString(name) {
		return c, false
	}
	info, err := os.Lstat(filepath.Join(c.base, c.token, name))
	if err != nil || info.Mode()&os.ModeSocket == 0 || !controlCleanupOwned(info) {
		return c, false
	}
	c.name, c.socketInfo = name, info
	root, ok := currentControlRoot(c, current)
	if !ok {
		return c, false
	}
	_ = root.Close()
	return c, refusedControlSocket(ctx, c.path())
}
