//go:build linux

package files

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestUsageEnumeratedChildDisappearanceKeepsRemainingAllocation(t *testing.T) {
	for _, directoryChild := range []bool{false, true} {
		t.Run(fmt.Sprint(directoryChild), func(t *testing.T) {
			directory := t.TempDir()
			if err := os.WriteFile(filepath.Join(directory, "keeper"), make([]byte, 8192), 0600); err != nil {
				t.Fatal(err)
			}
			gone := filepath.Join(directory, "gone")
			if directoryChild {
				if err := os.Mkdir(gone, 0700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(gone, []byte("transient"), 0600); err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(directory)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			original := diskStat
			defer func() { diskStat = original }()
			disappeared := false
			diskStat = func(fd int, name string, flags int) (unix.Statx_t, error) {
				if name == "gone" && !disappeared {
					disappeared = true
					if directoryChild {
						stat, err := original(fd, name, flags)
						if err := os.Remove(gone); err != nil {
							t.Fatal(err)
						}
						return stat, err // the subsequent strict Openat sees ENOENT
					}
					if err := os.Remove(gone); err != nil {
						t.Fatal(err)
					}
				}
				return original(fd, name, flags)
			}
			usage, err := MeasureUsage(t.Context(), root)
			if err != nil || !disappeared || usage.Inodes < 2 {
				t.Fatalf("benign child disappearance lost the pinned parent: %+v, %v", usage, err)
			}
			keeper, err := os.Stat(filepath.Join(directory, "keeper"))
			if err != nil || usage.Allocated < keeper.Sys().(*syscall.Stat_t).Blocks*512 {
				t.Fatal("remaining known allocation became zero")
			}
		})
	}
}

func TestUsageReplacementForeignOpenedMountAndEIONeverBecomeDisappearance(t *testing.T) {
	for _, mode := range []string{"replacement", "opened-mount", "io"} {
		t.Run(mode, func(t *testing.T) {
			directory := t.TempDir()
			swap := filepath.Join(directory, "swap")
			if err := os.Mkdir(swap, 0700); err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(directory)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			original := diskStat
			defer func() { diskStat = original }()
			var child uint64
			diskStat = func(fd int, name string, flags int) (unix.Statx_t, error) {
				stat, err := original(fd, name, flags)
				if name == "swap" {
					child = stat.Ino
					switch mode {
					case "replacement":
						if err := os.Rename(swap, filepath.Join(directory, "retired")); err != nil {
							t.Fatal(err)
						}
						if err := os.Mkdir(swap, 0700); err != nil {
							t.Fatal(err)
						}
					case "io":
						return unix.Statx_t{}, syscall.EIO
					}
				} else if name == "" && stat.Ino == child && mode == "opened-mount" {
					stat.Mnt_id++ // same device and inode, foreign mount AT the opened child
				}
				return stat, err
			}
			usage, err := MeasureUsage(t.Context(), root)
			if err == nil || usage != (Usage{}) {
				t.Fatalf("unknown or replaced child became measured zero: %+v %v", usage, err)
			}
			if mode == "io" && !errors.Is(err, syscall.EIO) || mode == "opened-mount" && !errors.Is(err, ErrDiskMount) ||
				mode == "replacement" && !strings.Contains(err.Error(), "changed while opening") {
				t.Fatalf("the precise failure was hidden: %v", err)
			}
		})
	}
	closed, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	closed.Close()
	if usage, err := MeasureUsage(t.Context(), closed); err == nil || usage != (Usage{}) {
		t.Fatalf("closed owned root became zero: %+v %v", usage, err)
	}
}

func TestUsageRealConcurrentFileChurnKeepsStableAllocation(t *testing.T) {
	directory := t.TempDir()
	keeper := filepath.Join(directory, "keeper")
	if err := os.WriteFile(keeper, make([]byte, 8192), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(keeper)
	if err != nil {
		t.Fatal(err)
	}
	minimum := info.Sys().(*syscall.Stat_t).Blocks * 512
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	stop, ready := make(chan struct{}), make(chan struct{})
	joined := make(chan error, 1)
	go func() {
		for i := range 2000 {
			select {
			case <-stop:
				joined <- nil
				return
			default:
			}
			name := filepath.Join(directory, fmt.Sprintf("transient-%d", i))
			if err := os.WriteFile(name, []byte("churn"), 0600); err != nil {
				joined <- err
				return
			}
			if i == 0 {
				close(ready)
			}
			if err := os.Remove(name); err != nil {
				joined <- err
				return
			}
		}
		joined <- nil
	}()
	defer func() {
		close(stop)
		if err := <-joined; err != nil {
			t.Error(err)
		}
	}()
	select {
	case <-ready:
	case err := <-joined:
		joined <- err
		select {
		case <-ready:
		default:
			t.Fatal("the owned churn writer did not start")
		}
	}
	for range 32 {
		usage, err := MeasureUsage(t.Context(), root)
		if err != nil || usage.Allocated < minimum || usage.Inodes < 2 {
			t.Fatalf("live regular-file deletion broke periodic allocation: %+v %v", usage, err)
		}
	}
}
