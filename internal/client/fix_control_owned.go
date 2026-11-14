package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/iamlakshya/simstim/internal/diag"
)

var epochSocketName = regexp.MustCompile(`^nf-[0-9a-f]{40}$`)

// ownedControlSocket binds a repair to the exact indexed epoch and inode.
// The marker key is opaque: downstream own/restricted variants use this
// same index without deriving their key from a pin here.
type ownedControlSocket struct {
	host                      Host
	base, scope               string
	hostKey, key, token, name string
	baseInfo, topInfo         os.FileInfo
	hostInfo, markerInfo      os.FileInfo
	epochInfo, socketInfo     os.FileInfo
}

func (c ownedControlSocket) marker() string {
	return filepath.Join("h", c.hostKey, c.key)
}
func (c ownedControlSocket) path() string {
	return filepath.Join(c.base, c.token, c.name)
}

// These seams observe only the already-owned candidate. They never widen
// discovery or grant ownership to a filename supplied by a test or caller.
var (
	controlCleanupOwned = controlFileOwned
	beforeControlRemove = func(string) {}
	probeOwnedControl   = func(ctx context.Context, path string) error {
		conn, err := (&net.Dialer{Timeout: 300 * time.Millisecond}).DialContext(ctx, "unix", path)
		if err == nil {
			_ = conn.Close()
		}
		return err
	}
)

func refusedControlSocket(ctx context.Context, path string) bool {
	if ctx.Err() != nil {
		return false
	}
	err := probeOwnedControl(ctx, path)
	return ctx.Err() == nil && !errors.Is(err, context.Canceled) &&
		!errors.Is(err, context.DeadlineExceeded) && errors.Is(err, syscall.ECONNREFUSED)
}

func privateControlInfo(info os.FileInfo) bool {
	return info != nil && info.IsDir() && info.Mode().Perm() == 0o700 && controlCleanupOwned(info)
}

// readOwnedControlMarker reads only a small regular no-follow file and
// retains the descriptor identity used for the ownership decision.
func readOwnedControlMarker(path string) (string, os.FileInfo, bool) {
	f, err := diag.OpenPlain(path)
	if err != nil {
		return "", nil, false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 ||
		info.Size() > controlKeyLen+1 || !controlCleanupOwned(info) {
		return "", nil, false
	}
	data, err := io.ReadAll(io.LimitReader(f, controlKeyLen+2))
	if err != nil {
		return "", nil, false
	}
	token, ok := strings.CutSuffix(string(data), "\n")
	return token, info, ok && controlKeyRE.MatchString(token)
}

// currentControlRoot checks the same root, saved entry, private directories,
// current marker and socket inode before any probe or unlink. The caller
// holds pin then hosts locks. A legacy empty marker confers no ownership.
func currentControlRoot(c ownedControlSocket, hosts []Host) (*os.Root, bool) {
	scope, ok := controlScope()
	if !ok || scope != c.scope || !slices.Contains(controlDirs(), c.base) ||
		!slices.ContainsFunc(hosts, func(h Host) bool { return h.Name == c.host.Name && SameEntry(h, c.host) }) {
		return nil, false
	}
	root, err := os.OpenRoot(c.base)
	if err != nil {
		return nil, false
	}
	good := false
	defer func() {
		if !good {
			_ = root.Close()
		}
	}()
	for _, item := range []struct {
		path string
		was  os.FileInfo
	}{{".", c.baseInfo}, {"h", c.topInfo}, {filepath.Join("h", c.hostKey), c.hostInfo}, {c.token, c.epochInfo}} {
		info, err := root.Lstat(item.path)
		if err != nil || !privateControlInfo(info) || !os.SameFile(info, item.was) {
			return nil, false
		}
	}
	token, marker, ok := readOwnedControlMarker(filepath.Join(c.base, c.marker()))
	if !ok || token != c.token || !os.SameFile(marker, c.markerInfo) {
		return nil, false
	}
	info, err := root.Lstat(filepath.Join(c.token, c.name))
	if err != nil || info.Mode()&os.ModeSocket == 0 || !controlCleanupOwned(info) || !os.SameFile(info, c.socketInfo) {
		return nil, false
	}
	epoch, err := os.OpenRoot(filepath.Join(c.base, c.token))
	if err != nil {
		return nil, false
	}
	info, err = epoch.Stat(".")
	if err != nil || !os.SameFile(info, c.epochInfo) {
		_ = epoch.Close()
		return nil, false
	}
	good = true
	_ = root.Close()
	return epoch, true
}

func withControlHost(ctx context.Context, host Host, run func([]Host) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	unlockPin, err := lockPin(ctx, host.Name)
	if err != nil {
		return err
	}
	defer unlockPin()
	unlockHosts, err := lockHostsFile(ctx)
	if err != nil {
		return err
	}
	defer unlockHosts()
	hosts, err := LoadHosts()
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return run(hosts)
}

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
			for _, base := range controlDirs() {
				baseInfo, err := os.Lstat(base)
				if err != nil || !privateControlInfo(baseInfo) {
					continue
				}
				top := filepath.Join(base, "h")
				topInfo, err := os.Lstat(top)
				if err != nil || !privateControlInfo(topInfo) {
					continue
				}
				hostKey := controlHostKey(host.Name)
				hostDir := filepath.Join(top, hostKey)
				hostInfo, err := os.Lstat(hostDir)
				if err != nil || !privateControlInfo(hostInfo) {
					continue
				}
				entries, err := os.ReadDir(hostDir)
				if err != nil {
					continue
				}
				for _, entry := range entries {
					if !controlKeyRE.MatchString(entry.Name()) {
						continue
					}
					token, marker, ok := readOwnedControlMarker(filepath.Join(hostDir, entry.Name()))
					if !ok {
						continue
					}
					epochInfo, err := os.Lstat(filepath.Join(base, token))
					if err != nil || !privateControlInfo(epochInfo) {
						continue
					}
					sockets, err := os.ReadDir(filepath.Join(base, token))
					if err != nil {
						continue
					}
					for _, socket := range sockets {
						if !epochSocketName.MatchString(socket.Name()) {
							continue
						}
						info, err := os.Lstat(filepath.Join(base, token, socket.Name()))
						if err != nil || info.Mode()&os.ModeSocket == 0 || !controlCleanupOwned(info) {
							continue
						}
						c := ownedControlSocket{host: host, base: base, scope: scope, hostKey: hostKey, key: entry.Name(), token: token, name: socket.Name(),
							baseInfo: baseInfo, topInfo: topInfo, hostInfo: hostInfo, markerInfo: marker, epochInfo: epochInfo, socketInfo: info}
						root, ok := currentControlRoot(c, current)
						if !ok {
							continue
						}
						_ = root.Close()
						if refusedControlSocket(ctx, c.path()) {
							found = append(found, c)
						}
					}
				}
			}
			return ctx.Err()
		})
		if err != nil && ctx.Err() != nil {
			break
		}
	}
	return found
}

func removeOwnedControlSocket(ctx context.Context, c ownedControlSocket) (bool, error) {
	scope, ok := controlScope()
	if !ok || scope != c.scope {
		return false, nil
	}
	removed := false
	err := withControlHost(ctx, c.host, func(hosts []Host) error {
		root, ok := currentControlRoot(c, hosts)
		if !ok {
			return nil
		}
		_ = root.Close()
		if !refusedControlSocket(ctx, c.path()) {
			return nil
		}
		beforeControlRemove(c.path())
		root, ok = currentControlRoot(c, hosts)
		if !ok {
			return nil
		}
		defer root.Close()
		if !refusedControlSocket(ctx, c.path()) {
			return nil
		}
		// Revalidate the current saved/index references after the testable
		// scheduling point, still under the same canonical locks.
		current, err := LoadHosts()
		if err != nil {
			return err
		}
		check, ok := currentControlRoot(c, current)
		if !ok {
			return nil
		}
		_ = check.Close()
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := root.Remove(c.name); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return fmt.Errorf("remove the owned shared SSH socket: %w\nTry: run doctor again once the shared SSH connection has stopped", err)
		}
		removed = true
		reportLocalRemoval(ctx, c.path())
		return nil
	})
	return removed, err
}

func runOwnedControlFix(ctx context.Context, candidates []ownedControlSocket) error {
	for _, candidate := range candidates {
		if _, err := removeOwnedControlSocket(ctx, candidate); err != nil {
			return err
		}
	}
	return nil
}
