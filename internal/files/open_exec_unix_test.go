//go:build linux || darwin

package files

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestRootedFileDescriptorChild(t *testing.T) {
	args := os.Args
	if len(args) < 3 || args[len(args)-2] != "root-fd-fixture" {
		return
	}
	var wanted unix.Stat_t
	if unix.Stat(args[len(args)-1], &wanted) != nil {
		os.Exit(3)
	}
	directory := "/dev/fd"
	if _, err := os.Stat("/proc/self/fd"); err == nil {
		directory = "/proc/self/fd"
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		os.Exit(3)
	}
	for _, entry := range entries {
		fd, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		var actual unix.Stat_t
		if unix.Fstat(fd, &actual) == nil && actual.Dev == wanted.Dev && actual.Ino == wanted.Ino {
			os.Exit(2) // actual inherited root directory, not a guessed FD number
		}
	}
	os.Exit(0)
}

func TestRootedRegularOpenDoesNotLeakRootAcrossConcurrentExec(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "data"), []byte("value"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	stop := make(chan struct{})
	failed := make(chan error, 8)
	var workers sync.WaitGroup
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				file, err := openRegularFile(root, "data")
				if err != nil {
					failed <- err
					return
				}
				file.Close()
			}
		}()
	}
	defer func() { close(stop); workers.Wait() }()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for range 256 {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		child := exec.CommandContext(ctx, executable, "-test.run=^TestRootedFileDescriptorChild$", "--", "root-fd-fixture", directory)
		err := child.Run()
		cancel()
		if err != nil {
			t.Fatal("concurrent child inherited the root descriptor or could not inspect its own descriptors", err)
		}
		select {
		case err := <-failed:
			t.Fatal(err)
		default:
		}
	}
}
