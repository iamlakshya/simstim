//go:build !windows

package client

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
)

func indexedControlPath(t *testing.T, name, key string) string {
	t.Helper()
	sshDirs(t)
	h := Host{Name: name, SSH: name + ".example"}
	if err := SaveHosts(context.Background(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	return indexedControlFixture(t, h, key)
}

func oneOwnedControl(t *testing.T, path string) ownedControlSocket {
	t.Helper()
	list := ownedControlSockets(context.Background())
	if len(list) != 1 || list[0].path() != path {
		t.Fatalf("owned candidates = %+v, want only %s", list, path)
	}
	return list[0]
}

func captureControlRemoval(t *testing.T) (context.Context, *[]string) {
	t.Helper()
	var paths []string
	return withLocalRemovalReport(context.Background(), func(path string) { paths = append(paths, path) }), &paths
}

func TestControlCleanupUsesOnlyCurrentRootIndexedEpochs(t *testing.T) {
	path := indexedControlPath(t, "box", strings.Repeat("k", 26))
	deadControlSocket(t, path)
	unknown := filepath.Join(filepath.Dir(filepath.Dir(path)), "custom-dead")
	deadControlSocket(t, unknown)
	legacy := filepath.Join(filepath.Dir(filepath.Dir(path)), "nf-"+strings.Repeat("b", 40))
	deadControlSocket(t, legacy)
	var probed []string
	probe := probeOwnedControl
	probeOwnedControl = func(ctx context.Context, path string) error { probed = append(probed, path); return probe(ctx, path) }
	t.Cleanup(func() { probeOwnedControl = probe })
	candidate := oneOwnedControl(t, path)
	if slices.Contains(probed, unknown) || slices.Contains(probed, legacy) {
		t.Fatalf("probed an unindexed path: %q", probed)
	}
	ctx, removed := captureControlRemoval(t)
	if changed, err := removeOwnedControlSocket(ctx, candidate); !changed || err != nil {
		t.Fatalf("owned cleanup = %v, %v", changed, err)
	}
	if !slices.Equal(*removed, []string{path}) {
		t.Fatalf("removed reports = %q", *removed)
	}
	for _, kept := range []string{unknown, legacy} {
		if _, err := os.Lstat(kept); err != nil {
			t.Fatalf("unindexed path changed: %s: %v", kept, err)
		}
	}
}

func TestControlCleanupKeepsReplacedAndLiveSockets(t *testing.T) {
	for _, kind := range []string{"regular file", "live replacement", "same inode becomes live"} {
		t.Run(kind, func(t *testing.T) {
			path := indexedControlPath(t, "box", strings.Repeat("k", 26))
			var fd int = -1
			if kind == "same inode becomes live" {
				var err error
				fd, err = syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = syscall.Close(fd) })
				if err := syscall.Bind(fd, &syscall.SockaddrUnix{Name: path}); err != nil {
					t.Fatal(err)
				}
			} else {
				deadControlSocket(t, path)
			}
			candidate := oneOwnedControl(t, path)
			hook := beforeControlRemove
			beforeControlRemove = func(path string) {
				if kind == "same inode becomes live" {
					if err := syscall.Listen(fd, 1); err != nil {
						t.Fatal(err)
					}
					return
				}
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if kind == "regular file" {
					if err := os.WriteFile(path, []byte("foreign data"), 0600); err != nil {
						t.Fatal(err)
					}
					return
				}
				listener, err := net.ListenUnix("unix", &net.UnixAddr{Net: "unix", Name: path})
				if err != nil {
					t.Fatal(err)
				}
				listener.SetUnlinkOnClose(false)
				t.Cleanup(func() { _ = listener.Close() })
			}
			t.Cleanup(func() { beforeControlRemove = hook })
			ctx, removed := captureControlRemoval(t)
			if changed, err := removeOwnedControlSocket(ctx, candidate); changed || err != nil {
				t.Fatalf("replaced/live cleanup = %v,%v", changed, err)
			}
			if len(*removed) != 0 {
				t.Fatalf("reported a preserved replacement: %q", *removed)
			}
			if _, err := os.Lstat(path); err != nil {
				t.Fatalf("removed replacement/live socket: %v", err)
			}
			if kind == "regular file" {
				if b, err := os.ReadFile(path); err != nil || string(b) != "foreign data" {
					t.Fatalf("foreign data changed: %q,%v", b, err)
				}
			}
		})
	}
}

func TestControlCleanupRechecksIndexHostAndRoot(t *testing.T) {
	for _, kind := range []string{"marker inode", "marker token", "saved entry", "other root"} {
		t.Run(kind, func(t *testing.T) {
			path := indexedControlPath(t, "box", strings.Repeat("k", 26))
			deadControlSocket(t, path)
			candidate := oneOwnedControl(t, path)
			hook := beforeControlRemove
			beforeControlRemove = func(string) {
				marker := filepath.Join(candidate.base, candidate.marker())
				switch kind {
				case "marker inode":
					tmp := marker + ".replacement"
					if err := os.WriteFile(tmp, []byte(candidate.token+"\n"), 0600); err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(tmp, marker); err != nil {
						t.Fatal(err)
					}
				case "marker token":
					if err := os.WriteFile(marker, []byte(strings.Repeat("b", 26)+"\n"), 0600); err != nil {
						t.Fatal(err)
					}
				case "saved entry":
					changed := candidate.host
					changed.SSH = "replacement.example"
					if err := writeHostsFile([]Host{changed}); err != nil {
						t.Fatal(err)
					}
				case "other root":
					t.Setenv("XDG_CONFIG_HOME", t.TempDir())
					if err := os.MkdirAll(ConfigDir(), 0700); err != nil {
						t.Fatal(err)
					}
					if err := writeHostsFile([]Host{candidate.host}); err != nil {
						t.Fatal(err)
					}
				}
			}
			t.Cleanup(func() { beforeControlRemove = hook })
			ctx, removed := captureControlRemoval(t)
			if changed, err := removeOwnedControlSocket(ctx, candidate); changed || err != nil {
				t.Fatalf("changed reference cleanup=%v,%v", changed, err)
			}
			if len(*removed) != 0 {
				t.Fatalf("reported changed reference removal: %q", *removed)
			}
			if _, err := os.Lstat(path); err != nil {
				t.Fatalf("changed reference socket went: %v", err)
			}
		})
	}
}

func TestControlCleanupRejectsForeignOwnershipAndCancelledVerdicts(t *testing.T) {
	path := indexedControlPath(t, "box", strings.Repeat("k", 26))
	deadControlSocket(t, path)
	candidate := oneOwnedControl(t, path)
	owner := controlCleanupOwned
	t.Cleanup(func() { controlCleanupOwned = owner })
	controlCleanupOwned = func(info os.FileInfo) bool {
		if info.Mode()&os.ModeSocket != 0 {
			return false
		}
		return owner(info)
	}
	if list := ownedControlSockets(context.Background()); len(list) != 0 {
		t.Fatal("adopted a foreign socket")
	}
	controlCleanupOwned = owner
	for _, kind := range []string{"cancelled", "wrapped deadline", "permission", "missing"} {
		t.Run(kind, func(t *testing.T) {
			ctx, removed := captureControlRemoval(t)
			probe := probeOwnedControl
			switch kind {
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "wrapped deadline":
				probeOwnedControl = func(context.Context, string) error {
					return errors.Join(syscall.ECONNREFUSED, context.DeadlineExceeded)
				}
			case "permission":
				probeOwnedControl = func(context.Context, string) error { return os.ErrPermission }
			case "missing":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			t.Cleanup(func() { probeOwnedControl = probe })
			changed, err := removeOwnedControlSocket(ctx, candidate)
			if changed || err != nil && kind != "cancelled" {
				t.Fatalf("unsafe verdict removed path: %v,%v", changed, err)
			}
			if len(*removed) != 0 {
				t.Fatalf("unsafe verdict reported removal: %q", *removed)
			}
		})
	}
}
